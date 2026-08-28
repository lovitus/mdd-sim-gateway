// SPDX-License-Identifier: AGPL-3.0-only

// Package usernet connects an SWu inner-packet session to an in-memory gVisor
// TCP/IP stack. It never creates an OS interface or changes a host route.
package usernet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/tun"
	wgnetstack "golang.zx2c4.com/wireguard/tun/netstack"
)

const (
	defaultMTU          = 1400
	defaultCloseTimeout = 5 * time.Second
	maximumCloseTimeout = 30 * time.Second
	maximumPacketSize   = 65535
)

var (
	ErrInvalidConfig = errors.New("invalid userspace network config")
	ErrClosed        = errors.New("userspace network is closed")
)

type PacketSession interface {
	Send(context.Context, []byte) error
	Receive(context.Context) ([]byte, error)
	Close(context.Context) error
}

type Config struct {
	Addresses    []netip.Addr
	DNS          []netip.Addr
	MTU          int
	CloseTimeout time.Duration
}

type PumpDirection string

const (
	PumpToSWu   PumpDirection = "to_swu"
	PumpFromSWu PumpDirection = "from_swu"
)

type PumpError struct {
	Direction PumpDirection
	Err       error
}

func (failure *PumpError) Error() string {
	return fmt.Sprintf("userspace packet pump %s: %v", failure.Direction, failure.Err)
}

func (failure *PumpError) Unwrap() error { return failure.Err }

type Stack struct {
	packets      PacketSession
	device       tun.Device
	network      *wgnetstack.Net
	closeTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wait   sync.WaitGroup
	done   chan struct{}
	errors chan error

	shutdownOnce sync.Once
	shutdownMu   sync.Mutex
	shutdownErr  error
}

func Open(ctx context.Context, packets PacketSession, config Config) (*Stack, error) {
	if packets == nil {
		return nil, fmt.Errorf("%w: packet session is nil", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	device, network, err := wgnetstack.CreateNetTUN(config.Addresses, config.DNS, config.MTU)
	if err != nil {
		return nil, fmt.Errorf("open in-memory TCP/IP stack: %w", err)
	}
	runContext, cancel := context.WithCancel(context.Background())
	stack := &Stack{
		packets: packets, device: device, network: network,
		closeTimeout: config.CloseTimeout,
		ctx:          runContext, cancel: cancel, done: make(chan struct{}), errors: make(chan error, 1),
	}
	stack.wait.Add(2)
	go stack.pumpToSWu()
	go stack.pumpFromSWu()
	go func() {
		stack.wait.Wait()
		close(stack.done)
		close(stack.errors)
	}()
	return stack, nil
}

func (stack *Stack) Errors() <-chan error {
	if stack == nil {
		closed := make(chan error)
		close(closed)
		return closed
	}
	return stack.errors
}

func (stack *Stack) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := stack.available(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	family, transport, err := parseNetwork(network)
	if err != nil {
		return nil, err
	}
	host, port, err := splitAddress(address)
	if err != nil {
		return nil, err
	}
	addresses, err := stack.LookupNetIP(ctx, family, host)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, address := range addresses {
		endpoint := netip.AddrPortFrom(address, port)
		switch transport {
		case "tcp":
			connection, err := stack.network.DialContextTCPAddrPort(ctx, endpoint)
			if err == nil {
				return connection, nil
			}
			failures = append(failures, err)
		case "udp":
			connection, err := stack.network.DialUDPAddrPort(netip.AddrPort{}, endpoint)
			if err == nil {
				if contextErr := ctx.Err(); contextErr != nil {
					_ = connection.Close()
					return nil, contextErr
				}
				return connection, nil
			}
			failures = append(failures, err)
		}
	}
	return nil, fmt.Errorf("dial %s %s: %w", network, address, errors.Join(failures...))
}

func (stack *Stack) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	if err := stack.available(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	family, transport, err := parseNetwork(network)
	if err != nil {
		return nil, err
	}
	if transport != "tcp" {
		return nil, fmt.Errorf("%w: Listen requires TCP", ErrInvalidConfig)
	}
	endpoint, err := stack.localEndpoint(family, address)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return stack.network.ListenTCPAddrPort(endpoint)
}

func (stack *Stack) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	if err := stack.available(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	family, transport, err := parseNetwork(network)
	if err != nil {
		return nil, err
	}
	if transport != "udp" {
		return nil, fmt.Errorf("%w: ListenPacket requires UDP", ErrInvalidConfig)
	}
	endpoint, err := stack.localEndpoint(family, address)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return stack.network.ListenUDPAddrPort(endpoint)
}

func (stack *Stack) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if err := stack.available(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	family, err := parseIPNetwork(network)
	if err != nil {
		return nil, err
	}
	host = strings.TrimSpace(host)
	if address, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if matchesFamily(address, family) {
			return []netip.Addr{address}, nil
		}
		return nil, &net.DNSError{Err: "address family mismatch", Name: host, IsNotFound: true}
	}
	values, err := stack.network.LookupContextHost(ctx, host)
	if err != nil {
		return nil, err
	}
	addresses := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		address, err := netip.ParseAddr(value)
		if err == nil && matchesFamily(address, family) {
			addresses = append(addresses, address)
		}
	}
	if len(addresses) == 0 {
		return nil, &net.DNSError{Err: "no address for requested family", Name: host, IsNotFound: true}
	}
	return addresses, nil
}

func (stack *Stack) Close(ctx context.Context) error {
	if stack == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stack.shutdown()
	select {
	case <-stack.done:
		stack.shutdownMu.Lock()
		defer stack.shutdownMu.Unlock()
		return stack.shutdownErr
	case <-ctx.Done():
		return errors.Join(ctx.Err(), stack.currentShutdownError())
	}
}

func (stack *Stack) pumpToSWu() {
	defer stack.wait.Done()
	buffer := make([]byte, maximumPacketSize)
	for {
		sizes := []int{0}
		count, err := stack.device.Read([][]byte{buffer}, sizes, 0)
		if err != nil {
			stack.fail(PumpToSWu, err)
			return
		}
		for index := 0; index < count; index++ {
			if sizes[index] <= 0 || sizes[index] > len(buffer) {
				stack.fail(PumpToSWu, errors.New("in-memory stack returned an invalid packet size"))
				return
			}
			if err := stack.packets.Send(stack.ctx, buffer[:sizes[index]]); err != nil {
				stack.fail(PumpToSWu, err)
				return
			}
		}
	}
}

func (stack *Stack) pumpFromSWu() {
	defer stack.wait.Done()
	for {
		packet, err := stack.packets.Receive(stack.ctx)
		if err != nil {
			stack.fail(PumpFromSWu, err)
			return
		}
		if len(packet) == 0 || len(packet) > maximumPacketSize {
			stack.fail(PumpFromSWu, errors.New("SWu returned an invalid inner packet size"))
			return
		}
		if _, err := stack.device.Write([][]byte{packet}, 0); err != nil {
			stack.fail(PumpFromSWu, err)
			return
		}
	}
}

func (stack *Stack) fail(direction PumpDirection, err error) {
	if stack.ctx.Err() == nil {
		select {
		case stack.errors <- &PumpError{Direction: direction, Err: err}:
		default:
		}
	}
	stack.shutdown()
}

func (stack *Stack) shutdown() {
	stack.shutdownOnce.Do(func() {
		stack.cancel()
		deviceErr := stack.device.Close()
		closeContext, cancel := context.WithTimeout(context.Background(), stack.closeTimeout)
		packetErr := stack.packets.Close(closeContext)
		cancel()
		stack.shutdownMu.Lock()
		stack.shutdownErr = errors.Join(deviceErr, packetErr)
		stack.shutdownMu.Unlock()
	})
}

func (stack *Stack) currentShutdownError() error {
	stack.shutdownMu.Lock()
	defer stack.shutdownMu.Unlock()
	return stack.shutdownErr
}

func (stack *Stack) available() error {
	if stack == nil || stack.network == nil || stack.ctx.Err() != nil {
		return ErrClosed
	}
	return nil
}

func (stack *Stack) localEndpoint(family, address string) (netip.AddrPort, error) {
	host, port, err := splitAddress(address)
	if err != nil {
		return netip.AddrPort{}, err
	}
	if host == "" {
		if family == "ip6" {
			return netip.AddrPortFrom(netip.IPv6Unspecified(), port), nil
		}
		return netip.AddrPortFrom(netip.IPv4Unspecified(), port), nil
	}
	parsed, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !matchesFamily(parsed, family) {
		return netip.AddrPort{}, fmt.Errorf("%w: invalid local address %q", ErrInvalidConfig, address)
	}
	return netip.AddrPortFrom(parsed, port), nil
}

func normalizeConfig(config Config) (Config, error) {
	if len(config.Addresses) == 0 {
		return Config{}, fmt.Errorf("%w: no inner address", ErrInvalidConfig)
	}
	for _, address := range config.Addresses {
		if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() {
			return Config{}, fmt.Errorf("%w: invalid inner address %q", ErrInvalidConfig, address)
		}
	}
	for _, address := range config.DNS {
		if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() {
			return Config{}, fmt.Errorf("%w: invalid DNS address %q", ErrInvalidConfig, address)
		}
	}
	if config.MTU == 0 {
		config.MTU = defaultMTU
	}
	if config.MTU < 576 || config.MTU > maximumPacketSize {
		return Config{}, fmt.Errorf("%w: invalid MTU %d", ErrInvalidConfig, config.MTU)
	}
	if config.CloseTimeout == 0 {
		config.CloseTimeout = defaultCloseTimeout
	}
	if config.CloseTimeout < 0 || config.CloseTimeout > maximumCloseTimeout {
		return Config{}, fmt.Errorf("%w: invalid close timeout", ErrInvalidConfig)
	}
	config.Addresses = append([]netip.Addr(nil), config.Addresses...)
	config.DNS = append([]netip.Addr(nil), config.DNS...)
	return config, nil
}

func parseNetwork(network string) (family, transport string, err error) {
	network = strings.ToLower(strings.TrimSpace(network))
	switch network {
	case "tcp", "udp":
		return "ip", network, nil
	case "tcp4", "udp4":
		return "ip4", network[:3], nil
	case "tcp6", "udp6":
		return "ip6", network[:3], nil
	default:
		return "", "", fmt.Errorf("%w: unsupported network %q", ErrInvalidConfig, network)
	}
}

func parseIPNetwork(network string) (string, error) {
	switch network = strings.ToLower(strings.TrimSpace(network)); network {
	case "ip", "ip4", "ip6":
		return network, nil
	default:
		return "", fmt.Errorf("%w: unsupported IP network %q", ErrInvalidConfig, network)
	}
}

func splitAddress(address string) (string, uint16, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", 0, fmt.Errorf("%w: invalid socket address %q", ErrInvalidConfig, address)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("%w: invalid port in %q", ErrInvalidConfig, address)
	}
	return host, uint16(port), nil
}

func matchesFamily(address netip.Addr, family string) bool {
	return family == "ip" || family == "ip4" && address.Is4() || family == "ip6" && address.Is6()
}
