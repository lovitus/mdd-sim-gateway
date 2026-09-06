//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
	"golang.org/x/sys/unix"
)

type dataClaim struct {
	target                                                    agentdata.Target
	uid                                                       string
	bearer                                                    dataBearer
	route                                                     linuxdataguard.DataRoute
	dns                                                       []netip.Addr
	profile                                                   string
	cleanup                                                   bool
	failures                                                  uint32
	retryAt                                                   time.Time
	permitClosed, routeCleaned, bearerDisconnected, inhibited bool
}

func (prober *Prober) PrepareData(ctx context.Context, target agentdata.Target, profile agentdata.Profile) (string, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if prober.guard == nil || prober.data == nil {
		return "", errors.New("persistent Linux cellular data guard is unavailable")
	}
	if current := prober.data[target.EquipmentID]; current != nil {
		if current.target == target && !current.cleanup {
			return current.profile, nil
		}
		return "", errors.New("another cellular data session owns this modem")
	}
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return "", err
	}
	var selected *agentmodem.Fact
	for index := range facts {
		fact := &facts[index]
		if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID && fact.SIM.ICCID == target.CardID &&
			(target.SIMSessionGeneration == "" || fact.SIM.SessionGeneration == target.SIMSessionGeneration) {
			if selected != nil {
				return "", agentmodem.ErrOperationTargetReplaced
			}
			selected = fact
		}
	}
	if selected == nil || selected.Condition != agentmodem.DeviceReady || selected.SIM.State != agentmodem.SIMReady ||
		selected.AT.State != agentmodem.ATControlReady || !selected.Capabilities.CellularData ||
		selected.Network.Guard.State != agentmodem.DataGuardProtected || selected.Network.Data != agentmodem.DataDisconnected {
		return "", agentmodem.ErrOperationUnavailable
	}
	if selected.Network.Registration == agentmodem.RegistrationRoaming && !profile.AllowRoaming {
		return "", errors.New("data roaming is disabled by modem policy")
	}
	var owned *ownedDevice
	for _, candidate := range prober.devices {
		if candidate.usb.AttachmentID == target.AttachmentID && candidate.snapshot.EquipmentID == target.EquipmentID {
			if owned != nil {
				return "", agentmodem.ErrOperationTargetReplaced
			}
			owned = candidate
		}
	}
	if owned == nil {
		return "", agentmodem.ErrOperationTargetReplaced
	}
	status, err := prober.at.SIMPINStatusFresh(ctx, target.EquipmentID)
	if err != nil || status.CardID != target.CardID {
		return "", agentmodem.ErrOperationTargetReplaced
	}
	if selected.AT.CallSignalling {
		call, err := prober.at.CallStatus(ctx, target.EquipmentID)
		if err != nil || !call.Authoritative || call.State != "idle" {
			return "", errors.New("modem call state is not authoritatively idle")
		}
	}
	permit, err := prober.guard.OpenDataPermit(ctx, target.EquipmentID+"\x00"+target.CardID)
	if err != nil {
		return "", err
	}
	claim := &dataClaim{target: target, uid: owned.snapshot.UID, route: linuxdataguard.DataRoute{Permit: permit}}
	prober.data[target.EquipmentID] = claim
	rollback := func(cause error) (string, error) {
		claim.cleanup = true
		cleanupErr := prober.cleanupDataClaim(claim)
		return "", errors.Join(cause, cleanupErr)
	}
	prober.at.Reconcile(ctx, prober.targetsExcept(map[string]struct{}{target.EquipmentID: {}}))
	if err := prober.manager.Inhibit(ctx, claim.uid, false); err != nil {
		return rollback(err)
	}
	snapshot, err := prober.awaitDataModem(ctx, claim.uid, target)
	if err != nil {
		return rollback(err)
	}
	claim.bearer, err = prober.manager.Connect(ctx, snapshot.ObjectPath, profile)
	if err != nil {
		return rollback(err)
	}
	address, err := netip.ParsePrefix(claim.bearer.Address + "/" + strconv.FormatUint(uint64(claim.bearer.Prefix), 10))
	if err != nil || !address.Addr().Is4() {
		return rollback(errors.New("ModemManager returned an invalid static IPv4 address"))
	}
	var gateway netip.Addr
	if strings.TrimSpace(claim.bearer.Gateway) != "" {
		gateway, err = netip.ParseAddr(claim.bearer.Gateway)
		if err != nil || !gateway.Is4() {
			return rollback(errors.New("ModemManager returned an invalid IPv4 gateway"))
		}
	}
	claim.route, err = prober.guard.ConfigureDataRoute(ctx, permit, claim.bearer.Interface, address, gateway)
	if err != nil {
		return rollback(err)
	}
	for _, value := range claim.bearer.DNS {
		if address, parseErr := netip.ParseAddr(value); parseErr == nil && address.Is4() {
			claim.dns = append(claim.dns, address)
		}
	}
	claim.profile = strings.TrimSpace(profile.Name)
	if claim.profile == "" {
		claim.profile = strings.TrimSpace(profile.APN)
	}
	if claim.profile == "" {
		claim.profile = "automatic"
	}
	prober.data[target.EquipmentID] = claim
	return claim.profile, nil
}

func (prober *Prober) awaitDataModem(ctx context.Context, uid string, target agentdata.Target) (modemSnapshot, error) {
	deadline := time.Now().Add(20 * time.Second)
	for {
		inventory, err := prober.manager.Inventory(ctx)
		if err == nil {
			matches := make([]modemSnapshot, 0, 1)
			for _, snapshot := range inventory {
				if snapshot.UID == uid && snapshot.EquipmentID == target.EquipmentID && snapshot.ICCID == target.CardID &&
					snapshot.SIMState == agentmodem.SIMReady {
					matches = append(matches, snapshot)
				}
			}
			if len(matches) == 1 {
				return matches[0], nil
			}
			if len(matches) > 1 {
				return modemSnapshot{}, errors.New("ModemManager data target is ambiguous")
			}
		}
		if !time.Now().Before(deadline) {
			return modemSnapshot{}, errors.New("exact ModemManager data target did not reappear")
		}
		select {
		case <-ctx.Done():
			return modemSnapshot{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (prober *Prober) DialData(ctx context.Context, target agentdata.Target, network, address string) (net.Conn, error) {
	prober.mu.Lock()
	claim := prober.data[target.EquipmentID]
	if claim == nil || claim.target != target {
		prober.mu.Unlock()
		return nil, agentmodem.ErrOperationTargetReplaced
	}
	copy := *claim
	copy.dns = append([]netip.Addr(nil), claim.dns...)
	prober.mu.Unlock()
	if network != "tcp" && network != "udp" {
		return nil, errors.New("unsupported cellular data socket network")
	}
	control := markedSocketControl(copy.bearer.Interface, copy.route.Permit.Mark)
	dialer := &net.Dialer{Control: control}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	targets := []string{address}
	if net.ParseIP(host) == nil {
		if len(copy.dns) == 0 {
			return nil, errors.New("cellular bearer returned no DNS server for a domain destination")
		}
		dnsAddress := net.JoinHostPort(copy.dns[0].String(), "53")
		resolver := &net.Resolver{PreferGo: true, Dial: func(resolveContext context.Context, resolveNetwork, _ string) (net.Conn, error) {
			if resolveNetwork != "udp" && resolveNetwork != "tcp" {
				return nil, errors.New("unsupported cellular DNS network")
			}
			return (&net.Dialer{Control: control}).DialContext(resolveContext, resolveNetwork, dnsAddress)
		}}
		addresses, resolveErr := resolver.LookupNetIP(ctx, "ip4", host)
		if resolveErr != nil || len(addresses) == 0 {
			return nil, errors.Join(errors.New("cellular DNS returned no IPv4 destination"), resolveErr)
		}
		targets = targets[:0]
		for _, resolved := range addresses {
			targets = append(targets, net.JoinHostPort(resolved.String(), port))
		}
	}
	var failures []error
	for _, destination := range targets {
		connection, dialErr := dialer.DialContext(ctx, network, destination)
		if dialErr == nil {
			return connection, nil
		}
		failures = append(failures, dialErr)
	}
	return nil, errors.Join(failures...)
}

func markedSocketControl(interfaceName string, mark uint32) func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var socketErr error
		if err := raw.Control(func(fd uintptr) {
			if err := unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, interfaceName); err != nil {
				socketErr = err
				return
			}
			socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark))
		}); err != nil {
			return err
		}
		return socketErr
	}
}

func (prober *Prober) StopData(_ context.Context, target agentdata.Target) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return prober.stopDataLocked(target)
}

func (prober *Prober) stopDataLocked(target agentdata.Target) error {
	claim := prober.data[target.EquipmentID]
	if claim == nil {
		return nil
	}
	if target.AttachmentID != "" && claim.target != target {
		return agentmodem.ErrOperationTargetReplaced
	}
	claim.cleanup = true
	return prober.cleanupDataClaim(claim)
}

func (prober *Prober) cleanupDataClaim(claim *dataClaim) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var failures []error
	if !claim.permitClosed {
		if err := prober.guard.CloseDataPermit(cleanupContext, claim.route.Permit); err != nil {
			failures = append(failures, err)
		} else {
			claim.permitClosed = true
		}
	}
	if claim.permitClosed && !claim.routeCleaned {
		if err := prober.guard.CleanupDataRoute(&claim.route); err != nil {
			failures = append(failures, err)
		} else {
			claim.routeCleaned = true
		}
	}
	if !claim.bearerDisconnected {
		if err := prober.manager.Disconnect(cleanupContext, claim.bearer.ObjectPath); err != nil {
			failures = append(failures, err)
		} else {
			claim.bearerDisconnected = true
		}
	}
	if !claim.inhibited {
		if err := prober.manager.Inhibit(cleanupContext, claim.uid, true); err != nil {
			failures = append(failures, err)
		} else {
			claim.inhibited = true
		}
	}
	if len(failures) == 0 && claim.permitClosed && claim.routeCleaned && claim.bearerDisconnected && claim.inhibited {
		at := prober.at.Reconcile(cleanupContext, prober.targetsExcept(nil))[claim.target.AttachmentID]
		if at.State == string(agentmodem.ATControlReady) {
			delete(prober.data, claim.target.EquipmentID)
			return nil
		}
		failures = append(failures, errors.New("exclusive AT owner has not recovered after data cleanup"))
	}
	claim.failures++
	delay := time.Second << min(claim.failures-1, 6)
	claim.retryAt = time.Now().Add(delay)
	return errors.Join(failures...)
}

func (prober *Prober) retryDataCleanup() {
	now := time.Now()
	for _, claim := range prober.data {
		if claim.cleanup && !now.Before(claim.retryAt) {
			_ = prober.cleanupDataClaim(claim)
		}
	}
}

func (prober *Prober) dataFact(current *ownedDevice, claim *dataClaim) agentmodem.Fact {
	fact := cloneFact(current.lastFact)
	continuity, continuityErr := prober.simContinuity(current)
	if fact.EquipmentID == "" {
		fact = agentmodem.Fact{AttachmentID: current.usb.AttachmentID, EquipmentID: current.snapshot.EquipmentID,
			ContinuityEpoch: continuity,
			Manufacturer:    current.snapshot.Manufacturer, Model: current.snapshot.Model, Firmware: current.snapshot.Firmware,
			SIM: agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: claim.target.CardID}}
	}
	fact.Condition = agentmodem.DeviceReady
	if continuityErr != nil {
		fact.ContinuityEpoch = ""
		fact.SIM = agentmodem.SIMFact{State: agentmodem.SIMUnknown}
		fact.Condition = agentmodem.DeviceDegraded
		fact.LastContinuityIssue = "sim_event_source_failed"
		fact.Detail = bounded(continuityErr.Error(), 1024)
	} else if fact.ContinuityEpoch != "" && fact.ContinuityEpoch != continuity {
		fact.ContinuityEpoch = continuity
		fact.SIM = agentmodem.SIMFact{State: agentmodem.SIMUnknown}
		fact.Condition = agentmodem.DeviceDegraded
		fact.LastContinuityIssue = "sim_insertion_changed"
		fact.Detail = "SIM insertion changed while protected cellular data was active"
	}
	fact.Capabilities.CellularData = true
	fact.AT = agentmodem.ATControlFact{State: agentmodem.ATControlUnavailable, Detail: "ModemManager owns the active data bearer"}
	fact.Network.Data = agentmodem.DataConnected
	fact.Network.Profile = claim.profile
	fact.Network.Guard = agentmodem.DataGuardFact{State: agentmodem.DataGuardProtected}
	if claim.cleanup {
		fact.Condition = agentmodem.DeviceDegraded
		fact.Detail = "cellular data cleanup is pending"
		fact.Capabilities.CellularData = false
		fact.Network.Data = agentmodem.DataUnknown
		if claim.bearerDisconnected {
			fact.Network.Data = agentmodem.DataDisconnected
		}
	}
	return fact
}

var _ agentdata.Backend = (*Prober)(nil)
