//go:build linux

package linuxmodem

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
)

type dhcpConfig struct {
	address                      netip.Prefix
	gateway                      netip.Addr
	dns                          []netip.Addr
	server                       netip.Addr
	renewAt, rebindAt, expiresAt time.Time
}

func dhcpIPv4(raw []byte) (netip.Addr, error) {
	if len(raw) != 4 {
		return netip.Addr{}, errors.New("DHCP IPv4 option has invalid length")
	}
	value := netip.AddrFrom4([4]byte(raw))
	if !value.IsGlobalUnicast() || value.IsLoopback() || value.IsLinkLocalUnicast() || raw[0] == 0 || raw[0] >= 224 {
		return netip.Addr{}, errors.New("DHCP IPv4 option is unusable")
	}
	return value, nil
}

func parseDHCPConfig(ack *dhcpv4.DHCPv4, requestAt time.Time, prior *dhcpConfig) (dhcpConfig, error) {
	var result dhcpConfig
	if ack == nil || requestAt.IsZero() || ack.MessageType() != dhcpv4.MessageTypeAck {
		return result, errors.New("invalid DHCP ACK")
	}
	address, err := dhcpIPv4(ack.YourIPAddr.To4())
	if err != nil {
		return result, err
	}
	result.server, err = dhcpIPv4(ack.Options.Get(dhcpv4.OptionServerIdentifier))
	if err != nil {
		return result, err
	}
	mask := ack.Options.Get(dhcpv4.OptionSubnetMask)
	if mask == nil && prior != nil {
		mask = net.CIDRMask(prior.address.Bits(), 32)
	}
	if len(mask) != 4 {
		return result, errors.New("DHCP subnet mask is missing or malformed")
	}
	ones, bits := net.IPMask(mask).Size()
	if bits != 32 || ones == 0 {
		return result, errors.New("DHCP subnet mask is not contiguous")
	}
	result.address = netip.PrefixFrom(address, ones)
	if ones < 31 {
		number := binary.BigEndian.Uint32(address.AsSlice())
		network := number & binary.BigEndian.Uint32(mask)
		if number == network || number == network|^binary.BigEndian.Uint32(mask) {
			return result, errors.New("DHCP address is a network or broadcast address")
		}
	}
	addresses := func(code dhcpv4.OptionCode, max int) ([]netip.Addr, error) {
		raw := ack.Options.Get(code)
		if raw == nil {
			return nil, nil
		}
		if len(raw) == 0 || len(raw)%4 != 0 || len(raw) > 4*max {
			return nil, errors.New("malformed DHCP address list")
		}
		var values []netip.Addr
		for at := 0; at < len(raw); at += 4 {
			value, err := dhcpIPv4(raw[at : at+4])
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	}
	routers, err := addresses(dhcpv4.OptionRouter, 8)
	if err != nil {
		return result, err
	}
	if len(routers) > 0 {
		result.gateway = routers[0]
	} else if prior != nil {
		result.gateway = prior.gateway
	}
	result.dns, err = addresses(dhcpv4.OptionDomainNameServer, 8)
	if err != nil {
		return result, err
	}
	if result.dns == nil && prior != nil {
		result.dns = append([]netip.Addr(nil), prior.dns...)
	}
	// DNS may legitimately be absent for IP-only traffic. Never use host DNS.
	duration := func(code dhcpv4.OptionCode, fallback uint32) (uint32, error) {
		raw := ack.Options.Get(code)
		if raw == nil {
			return fallback, nil
		}
		if len(raw) != 4 {
			return 0, errors.New("malformed DHCP lease time")
		}
		return binary.BigEndian.Uint32(raw), nil
	}
	lease, err := duration(dhcpv4.OptionIPAddressLeaseTime, 0)
	if err != nil || lease == 0 {
		return result, errors.New("DHCP lease lifetime is missing or malformed")
	}
	if lease == ^uint32(0) {
		return result, nil
	}
	t1, err := duration(dhcpv4.OptionRenewTimeValue, lease/2)
	if err != nil {
		return result, err
	}
	t2, err := duration(dhcpv4.OptionRebindingTimeValue, uint32(uint64(lease)*7/8))
	if err != nil {
		return result, err
	}
	if t1 == 0 || t1 >= t2 || t2 >= lease {
		return result, errors.New("DHCP lease deadlines are out of order")
	}
	result.renewAt = requestAt.Add(time.Duration(t1) * time.Second)
	result.rebindAt = requestAt.Add(time.Duration(t2) * time.Second)
	result.expiresAt = requestAt.Add(time.Duration(lease) * time.Second)
	if !time.Now().Before(result.expiresAt) {
		return result, errors.New("DHCP ACK arrived after lease expiry")
	}
	return result, nil
}

func sameDHCPNetwork(a, b dhcpConfig) bool {
	return a.address == b.address && a.gateway == b.gateway && slices.Equal(a.dns, b.dns)
}

type dynamicGuard interface {
	PrepareDynamicRoute(context.Context, linuxdataguard.DataPermit, net.Interface) (linuxdataguard.DataRoute, error)
	ConfigureDynamicRoute(context.Context, *linuxdataguard.DataRoute, netip.Prefix, netip.Addr) error
}

type dhcpClaim struct {
	mu          sync.Mutex
	config      dhcpConfig
	ack         *dhcpv4.DHCPv4
	iface       net.Interface
	invalid     chan struct{}
	done        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	once        sync.Once
	connections map[*dhcpDataConn]struct{}
	check       func(context.Context) error
	revoke      func() error
	err         error
	expiryTimer *time.Timer
}

func (claim *dhcpClaim) invalidate(cause error) {
	claim.once.Do(func() {
		claim.mu.Lock()
		claim.err = cause
		if claim.expiryTimer != nil {
			claim.expiryTimer.Stop()
		}
		close(claim.invalid)
		claim.cancel()
		connections := make([]*dhcpDataConn, 0, len(claim.connections))
		for connection := range claim.connections {
			connections = append(connections, connection)
		}
		claim.mu.Unlock()
		for _, connection := range connections {
			_ = connection.Close()
		}
		// Closing sockets is immediate even if the kernel permit removal fails.
		// The existing owner retains cleanup and retries its permit removal.
		if claim.revoke != nil {
			if err := claim.revoke(); err != nil {
				claim.mu.Lock()
				claim.err = errors.Join(cause, err)
				claim.mu.Unlock()
			}
		}
	})
}

func (claim *dhcpClaim) valid() bool {
	if claim == nil {
		return true
	}
	claim.mu.Lock()
	// Decide under the same lock as renewal commits, not from an old snapshot.
	expired := !claim.config.expiresAt.IsZero() && !time.Now().Before(claim.config.expiresAt)
	claim.mu.Unlock()
	if expired {
		claim.invalidate(errors.New("DHCP lease expired"))
	}
	return !agentdata.Invalidated(claim.invalid)
}

func (claim *dhcpClaim) stop() {
	claim.invalidate(context.Canceled)
	<-claim.done
}

type dhcpDataConn struct {
	net.Conn
	owner *dhcpClaim
	once  sync.Once
}

// Go's resolver distinguishes datagram DNS from stream DNS using PacketConn.
// Preserve that capability only for an underlying datagram connection.
type dhcpPacketConn struct {
	*dhcpDataConn
	packet net.PacketConn
}

func (connection *dhcpPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	return connection.packet.ReadFrom(b)
}

func (connection *dhcpPacketConn) WriteTo(b []byte, address net.Addr) (int, error) {
	return connection.packet.WriteTo(b, address)
}

func (connection *dhcpDataConn) Close() error {
	var err error
	connection.once.Do(func() {
		err = connection.Conn.Close()
		connection.owner.mu.Lock()
		delete(connection.owner.connections, connection)
		connection.owner.mu.Unlock()
	})
	return err
}

func (claim *dhcpClaim) track(connection net.Conn) (net.Conn, error) {
	if !claim.valid() {
		connection.Close()
		return nil, agentmodem.ErrOperationUnavailable
	}
	claim.mu.Lock()
	defer claim.mu.Unlock()
	if agentdata.Invalidated(claim.invalid) {
		connection.Close()
		return nil, agentmodem.ErrOperationUnavailable
	}
	wrapped := &dhcpDataConn{Conn: connection, owner: claim}
	claim.connections[wrapped] = struct{}{}
	if packet, ok := connection.(net.PacketConn); ok {
		return &dhcpPacketConn{dhcpDataConn: wrapped, packet: packet}, nil
	}
	return wrapped, nil
}

func (prober *Prober) prepareDHCP(ctx context.Context, claim *dataClaim, owned *ownedDevice) error {
	guard, ok := prober.guard.(dynamicGuard)
	if !ok {
		return errors.New("Linux guard cannot own DHCP routes")
	}
	iface, err := net.InterfaceByName(claim.bearer.Interface)
	if err != nil {
		return err
	}
	frozenUSB, frozenSnapshot, epoch := owned.usb, claim.commandSnapshot, claim.commandEpoch
	manager, sysRoot := prober.manager, prober.sysRoot
	check := func(checkContext context.Context) error {
		current, err := net.InterfaceByIndex(iface.Index)
		if err != nil || current.Name != iface.Name || !bytes.Equal(current.HardwareAddr, iface.HardwareAddr) || len(current.HardwareAddr) != 6 {
			return errors.New("DHCP interface was replaced")
		}
		kind, err := os.ReadFile(filepath.Join(sysRoot, "class", "net", iface.Name, "type"))
		if err != nil || strings.TrimSpace(string(kind)) != "1" {
			return errors.New("DHCP supports Ethernet links only")
		}
		group, err := os.ReadFile(filepath.Join(sysRoot, "class", "net", iface.Name, "netdev_group"))
		if err != nil || strings.TrimSpace(string(group)) != fmt.Sprint(linuxdataguard.InterfaceGroup) {
			return errors.New("DHCP interface isolation changed")
		}
		parent, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "class", "net", iface.Name, "device"))
		if err != nil || !sameUSBTree(parent, frozenUSB.PhysicalID) {
			return errors.New("DHCP interface USB parent changed")
		}
		generation, err := resolveUSBGeneration(sysRoot, frozenSnapshot.ATPorts)
		if err != nil || generation != frozenUSB {
			return errors.New("DHCP modem attachment changed")
		}
		inventory, err := manager.Inventory(checkContext)
		if err != nil {
			return err
		}
		matches := 0
		for _, snapshot := range inventory {
			if snapshot.UID == claim.uid && snapshot.ObjectPath == frozenSnapshot.ObjectPath && snapshot.EquipmentID == claim.target.EquipmentID && snapshot.ICCID == claim.target.CardID && snapshot.SIMState == agentmodem.SIMReady && snapshot.BearerStates[claim.bearer.ObjectPath] {
				matches++
			}
		}
		if matches != 1 {
			return errors.New("DHCP bearer identity is no longer exact")
		}
		if epoch == "" {
			return errors.New("DHCP requires a live SIM event fence")
		}
		port := modemCommandPort{manager: manager, snapshot: frozenSnapshot, epoch: epoch}
		return port.check(checkContext)
	}
	if err := check(ctx); err != nil {
		return err
	}
	claim.route, err = guard.PrepareDynamicRoute(ctx, claim.route.Permit, *iface)
	if err != nil {
		return err
	}
	transport, err := openDHCPTransport(ctx, *iface, nil, claim.route.Permit.Mark)
	if err != nil {
		return err
	}
	client, err := nclient4.NewWithConn(transport, iface.HardwareAddr, nclient4.WithTimeout(2*time.Second), nclient4.WithRetry(3))
	if err != nil {
		transport.Close()
		return err
	}
	acquire, cancel := context.WithTimeout(ctx, 12*time.Second)
	lease, err := client.Request(acquire)
	cancel()
	_ = client.Close()
	if err != nil {
		return fmt.Errorf("acquire modem DHCP lease: %w", err)
	}
	config, err := parseDHCPConfig(lease.ACK, transport.sentAt(lease.ACK.TransactionID), nil)
	if err != nil {
		return err
	}
	workerContext, workerCancel := context.WithCancel(context.Background())
	worker := &dhcpClaim{config: config, ack: lease.ACK, iface: *iface, invalid: make(chan struct{}), done: make(chan struct{}), ctx: workerContext, cancel: workerCancel, connections: make(map[*dhcpDataConn]struct{}), check: check}
	permit := claim.route.Permit
	worker.revoke = func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return prober.guard.CloseDataPermit(ctx, permit)
	}
	claim.dhcp = worker
	// The expiry owner exists before either readback or permit publication. Its
	// revocation serializes with publication in the guard, even if readback stalls.
	worker.scheduleExpiry()
	running := false
	defer func() {
		if !running {
			worker.invalidate(errors.New("DHCP preparation did not commit"))
			close(worker.done)
		}
	}()
	prepareContext, prepareCancel := context.WithCancel(ctx)
	if !config.expiresAt.IsZero() {
		prepareCancel()
		prepareContext, prepareCancel = context.WithDeadline(ctx, config.expiresAt)
	}
	defer prepareCancel()
	stopCancellation := context.AfterFunc(prepareContext, func() { worker.invalidate(prepareContext.Err()) })
	defer stopCancellation()
	if err := check(prepareContext); err != nil {
		return err
	}
	if err := guard.ConfigureDynamicRoute(prepareContext, &claim.route, config.address, config.gateway); err != nil {
		return err
	}
	if err := check(prepareContext); err != nil {
		return err
	}
	if err := prepareContext.Err(); err != nil {
		return err
	}
	if !worker.valid() || !stopCancellation() {
		return errors.New("DHCP preparation expired or was cancelled")
	}
	claim.bearer.Address = config.address.Addr().String()
	claim.bearer.Prefix = uint32(config.address.Bits())
	claim.bearer.Gateway = ""
	if config.gateway.IsValid() {
		claim.bearer.Gateway = config.gateway.String()
	}
	claim.bearer.DNS = nil
	for _, address := range config.dns {
		claim.bearer.DNS = append(claim.bearer.DNS, address.String())
	}
	running = true
	go worker.run(permit.Mark)
	return nil
}

func (claim *dhcpClaim) run(mark uint32) {
	defer close(claim.done)
	defer claim.invalidate(errors.New("DHCP worker stopped"))
	for {
		claim.mu.Lock()
		config, ack := claim.config, claim.ack
		claim.mu.Unlock()
		delay := time.Second
		if !config.renewAt.IsZero() && time.Until(config.renewAt) < delay {
			delay = max(time.Until(config.renewAt), 0)
		}
		timer := time.NewTimer(delay)
		select {
		case <-claim.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if !claim.valid() {
			return
		}
		checkContext, cancel := context.WithTimeout(claim.ctx, 2*time.Second)
		err := claim.check(checkContext)
		cancel()
		if err != nil {
			claim.invalidate(err)
			return
		}
		if config.renewAt.IsZero() || time.Now().Before(config.renewAt) {
			continue
		}
		updated, nextACK, err := claim.renew(config, ack, mark)
		if err != nil {
			claim.invalidate(err)
			return
		}
		claim.mu.Lock()
		if agentdata.Invalidated(claim.invalid) || !time.Now().Before(config.expiresAt) || (!updated.expiresAt.IsZero() && !time.Now().Before(updated.expiresAt)) {
			claim.mu.Unlock()
			return
		}
		claim.config, claim.ack = updated, nextACK
		claim.mu.Unlock()
		claim.scheduleExpiry()
	}
}

func (claim *dhcpClaim) renew(config dhcpConfig, ack *dhcpv4.DHCPv4, mark uint32) (dhcpConfig, *dhcpv4.DHCPv4, error) {
	transport, err := openDHCPTransport(claim.ctx, claim.iface, net.IP(config.address.Addr().AsSlice()), mark)
	if err != nil {
		return config, nil, err
	}
	client, err := nclient4.NewWithConn(transport, claim.iface.HardwareAddr, nclient4.WithTimeout(time.Second), nclient4.WithRetry(1))
	if err != nil {
		transport.Close()
		return config, nil, err
	}
	defer client.Close()
	for _, phase := range []struct {
		until  time.Time
		server netip.Addr
		rebind bool
	}{{config.rebindAt, config.server, false}, {config.expiresAt, netip.MustParseAddr("255.255.255.255"), true}} {
		if !time.Now().Before(phase.until) {
			continue
		}
		var xid dhcpv4.TransactionID
		if _, err := rand.Read(xid[:]); err != nil {
			return config, nil, err
		}
		request, err := dhcpv4.NewRenewFromAck(ack, func(value *dhcpv4.DHCPv4) { value.TransactionID = xid })
		if err != nil {
			return config, nil, err
		}
		for time.Now().Before(phase.until) {
			if !claim.valid() {
				return config, nil, errors.New("DHCP lease expired during renewal")
			}
			checkContext, cancel := context.WithTimeout(claim.ctx, 2*time.Second)
			err := claim.check(checkContext)
			cancel()
			if err != nil {
				return config, nil, err
			}
			until := minTime(phase.until, time.Now().Add(2*time.Second))
			exchange, cancel := context.WithDeadline(claim.ctx, until)
			// nclient4 writes before waiting on the context. Bound both raw T2
			// and UDP T1 writes by the existing exchange/lease deadline.
			if err := transport.SetWriteDeadline(until); err != nil {
				cancel()
				return config, nil, err
			}
			match := func(reply *dhcpv4.DHCPv4) bool {
				return (reply.MessageType() == dhcpv4.MessageTypeAck || reply.MessageType() == dhcpv4.MessageTypeNak) && (phase.rebind || bytes.Equal(reply.ServerIdentifier(), config.server.AsSlice()))
			}
			reply, err := client.SendAndRead(exchange, &net.UDPAddr{IP: net.IP(phase.server.AsSlice()), Port: 67}, request, match)
			cancel()
			if err == nil {
				if reply.MessageType() == dhcpv4.MessageTypeNak {
					return config, nil, errors.New("modem DHCP lease was revoked")
				}
				updated, err := parseDHCPConfig(reply, transport.sentAt(xid), &config)
				if err != nil {
					return config, nil, err
				}
				if !sameDHCPNetwork(config, updated) {
					return config, nil, errors.New("modem DHCP network configuration changed")
				}
				checkContext, cancel := context.WithDeadline(claim.ctx, minTime(config.expiresAt, time.Now().Add(2*time.Second)))
				err = claim.check(checkContext)
				cancel()
				if err != nil {
					return config, nil, err
				}
				if !claim.valid() {
					return config, nil, errors.New("late DHCP renewal after expiry")
				}
				return updated, reply, nil
			}
			if claim.ctx.Err() != nil {
				return config, nil, claim.ctx.Err()
			}
			pause := time.NewTimer(min(time.Second, time.Until(phase.until)))
			select {
			case <-claim.ctx.Done():
				pause.Stop()
				return config, nil, claim.ctx.Err()
			case <-pause.C:
			}
		}
	}
	return config, nil, errors.New("modem DHCP lease expired without renewal")
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (claim *dhcpClaim) scheduleExpiry() {
	claim.mu.Lock()
	defer claim.mu.Unlock()
	if claim.expiryTimer != nil {
		claim.expiryTimer.Stop()
	}
	if !agentdata.Invalidated(claim.invalid) && !claim.config.expiresAt.IsZero() {
		claim.expiryTimer = time.AfterFunc(time.Until(claim.config.expiresAt), func() { claim.valid() })
	}
}

func (prober *Prober) DataValidity(target agentdata.Target) (<-chan struct{}, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	claim := prober.data[target.EquipmentID]
	if claim == nil || claim.target != target {
		return nil, agentmodem.ErrOperationTargetReplaced
	}
	if claim.dhcp == nil {
		return nil, nil
	}
	claim.dhcp.valid()
	return claim.dhcp.invalid, nil
}
