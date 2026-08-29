// SPDX-License-Identifier: AGPL-3.0-only

// Package outerudp owns one connected NAT-T UDP association shared by IKE
// control traffic and ESP packets. It has no host-route or process authority.
package outerudp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boa-z/vowifi-go/engine/swu"
	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
	"github.com/txthinking/socks5"
)

const (
	defaultQueueCapacity = 64
	maximumDatagramSize  = 64 << 10
	proxyDNSTimeout      = 8 * time.Second
)

var proxyDNSServers = [...]string{"1.1.1.1:53", "8.8.8.8:53"}

var (
	ErrClosed        = errors.New("outer UDP transport is closed")
	ErrQueueOverflow = errors.New("outer UDP transport receive queue overflow")
	ErrNoEndpoint    = errors.New("no ePDG endpoint answered IKE_SA_INIT")
	ErrSelecting     = errors.New("outer UDP endpoint selection is in progress")
)

type DialContextFunc func(context.Context, string, string, time.Duration) (net.Conn, error)
type ResolveContextFunc func(context.Context, string, string) ([]netip.Addr, error)

type Config struct {
	ProxyURL        string
	QueueCapacity   int
	CandidateOffset uint64
	DialContext     DialContextFunc
	ResolveContext  ResolveContextFunc
}

type Transport struct {
	config Config

	mu       sync.Mutex
	remote   string
	selected string
	timeout  time.Duration
	conn     net.Conn
	pending  net.Conn
	closed   bool
	failure  error
	done     chan struct{}

	exchange chan struct{}
	writeMu  sync.Mutex
	ike      chan []byte
	esp      chan []byte
}

var (
	_ ikev2.InitTransport             = (*Transport)(nil)
	_ swu.ESPPacketReadWriteTransport = (*Transport)(nil)
	_ swu.ESPPacketTransportCloser    = (*Transport)(nil)
	_ swu.NATTKeepaliveSender         = (*Transport)(nil)
)

func New(config Config) (*Transport, error) {
	config.ProxyURL = strings.TrimSpace(config.ProxyURL)
	if config.ProxyURL != "" {
		if _, err := parseSOCKS5URL(config.ProxyURL); err != nil {
			return nil, err
		}
	}
	if config.QueueCapacity == 0 {
		config.QueueCapacity = defaultQueueCapacity
	}
	if config.QueueCapacity < 1 || config.QueueCapacity > 4096 {
		return nil, errors.New("outer UDP queue capacity is invalid")
	}
	if config.DialContext == nil {
		config.DialContext = dialContext
	}
	if config.ResolveContext == nil {
		if config.ProxyURL == "" {
			config.ResolveContext = net.DefaultResolver.LookupNetIP
		} else {
			config.ResolveContext = proxyResolveContext(config.ProxyURL, config.DialContext)
		}
	}
	return &Transport{
		config: config, done: make(chan struct{}), exchange: make(chan struct{}, 1),
		ike: make(chan []byte, config.QueueCapacity), esp: make(chan []byte, config.QueueCapacity),
	}, nil
}

// Bind fixes the exact ePDG endpoint before either factory exposes this
// transport. IKE and ESP must never silently bind different destinations.
func (transport *Transport) Bind(remote string, timeout time.Duration) error {
	remote = strings.TrimSpace(remote)
	if _, _, err := net.SplitHostPort(remote); err != nil {
		return errors.New("outer UDP remote address must include a port")
	}
	if timeout < 0 || timeout > 2*time.Minute {
		return errors.New("outer UDP timeout is invalid")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return ErrClosed
	}
	if transport.remote != "" && transport.remote != remote {
		return errors.New("outer UDP transport is already bound to another endpoint")
	}
	transport.remote, transport.timeout = remote, timeout
	return nil
}

func (transport *Transport) ExchangeIKE(ctx context.Context, request []byte) ([]byte, error) {
	if len(request) == 0 {
		return nil, errors.New("IKE request is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case transport.exchange <- struct{}{}:
		defer func() { <-transport.exchange }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-transport.done:
		return nil, transport.err()
	}
	for {
		select {
		case <-transport.ike:
			continue
		default:
		}
		break
	}
	transport.mu.Lock()
	connected := transport.conn != nil
	transport.mu.Unlock()
	if !connected {
		return transport.exchangeInitial(ctx, request)
	}
	wire := make([]byte, len(request)+4)
	copy(wire[4:], request)
	if err := transport.write(ctx, wire); err != nil {
		return nil, err
	}
	select {
	case response := <-transport.ike:
		return response, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-transport.done:
		return nil, transport.err()
	}
}

func (transport *Transport) SendESPPacket(ctx context.Context, packet []byte) error {
	if len(packet) < 8 || hasNonESPMarker(packet) {
		return errors.New("invalid outbound ESP packet")
	}
	return transport.write(ctx, packet)
}

func (transport *Transport) ReadESPPacket(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := transport.ensure(ctx); err != nil {
		return nil, err
	}
	select {
	case packet := <-transport.esp:
		return packet, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-transport.done:
		return nil, transport.err()
	}
}

func (transport *Transport) SendNATTKeepalive(ctx context.Context) error {
	return transport.write(ctx, []byte{0xff})
}

func (transport *Transport) LocalNetworkAddr() net.Addr {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.conn == nil {
		return nil
	}
	return transport.conn.LocalAddr()
}

// SelectedRemote reports the literal ePDG endpoint only after it answered the
// first IKE exchange. It is diagnostic state, not a new routing decision.
func (transport *Transport) SelectedRemote() string {
	if transport == nil {
		return ""
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.selected
}

func (transport *Transport) Close(context.Context) error {
	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		return nil
	}
	transport.closed = true
	conn := transport.conn
	pending := transport.pending
	transport.conn = nil
	transport.pending = nil
	close(transport.done)
	transport.mu.Unlock()
	if pending != nil && pending != conn {
		_ = pending.Close()
	}
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (transport *Transport) write(ctx context.Context, payload []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := transport.ensure(ctx)
	if err != nil {
		return err
	}
	transport.writeMu.Lock()
	defer transport.writeMu.Unlock()
	if err := conn.SetWriteDeadline(transport.deadline(ctx)); err != nil {
		return err
	}
	written, err := conn.Write(payload)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	if written != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}

func (transport *Transport) ensure(ctx context.Context) (net.Conn, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return nil, transport.errLocked()
	}
	if transport.conn != nil {
		return transport.conn, nil
	}
	if transport.pending != nil {
		return nil, ErrSelecting
	}
	if transport.remote == "" {
		return nil, errors.New("outer UDP transport is not bound")
	}
	remote := transport.remote
	if transport.selected != "" {
		remote = transport.selected
	}
	conn, err := transport.config.DialContext(ctx, transport.config.ProxyURL, remote, transport.timeout)
	if err != nil {
		return nil, err
	}
	if transport.closed {
		_ = conn.Close()
		return nil, transport.errLocked()
	}
	transport.conn = conn
	go transport.readLoop(conn)
	return conn, nil
}

func (transport *Transport) readLoop(conn net.Conn) {
	buffer := make([]byte, maximumDatagramSize)
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			transport.fail(err)
			return
		}
		if n == 0 || n == 1 && buffer[0] == 0xff {
			continue
		}
		packet := append([]byte(nil), buffer[:n]...)
		target := transport.esp
		if hasNonESPMarker(packet) {
			packet = packet[4:]
			target = transport.ike
		} else if isUnmarkedIKE(packet) {
			// RFC 7296 requires the Non-ESP marker on UDP 4500. Retain a
			// narrow compatibility path for peers that omit it, but never
			// route arbitrary early ESP ciphertext into the IKE parser.
			target = transport.ike
		}
		select {
		case target <- packet:
		case <-transport.done:
			return
		default:
			if target == transport.esp {
				// ESP is a lossy datagram stream. A transient slow consumer may
				// drop a packet, but must not tear down an otherwise healthy call.
				continue
			}
			transport.fail(ErrQueueOverflow)
			return
		}
	}
}

func isUnmarkedIKE(packet []byte) bool {
	header, err := ikev2.ParseHeader(packet)
	return err == nil && header.Version>>4 == 2 && header.Length == uint32(len(packet))
}

func (transport *Transport) fail(err error) {
	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		return
	}
	transport.closed = true
	transport.failure = err
	conn := transport.conn
	pending := transport.pending
	transport.conn = nil
	transport.pending = nil
	close(transport.done)
	transport.mu.Unlock()
	if pending != nil && pending != conn {
		_ = pending.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

// exchangeInitial selects an ePDG address only before the first IKE response.
// Once any candidate answers, all later IKE/EAP/ESP traffic is pinned to it;
// authentication or protocol failures must never trigger another SIM AKA run.
func (transport *Transport) exchangeInitial(ctx context.Context, request []byte) ([]byte, error) {
	candidates, timeout, err := transport.candidates(ctx)
	if err != nil {
		return nil, err
	}
	selectionCtx := ctx
	selectionCancel := func() {}
	if timeout > 0 {
		selectionCtx, selectionCancel = context.WithTimeout(ctx, timeout)
	}
	defer selectionCancel()

	wire := make([]byte, len(request)+4)
	copy(wire[4:], request)
	var failures []error
	for index, candidate := range candidates {
		if err := selectionCtx.Err(); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			break
		}
		attemptCtx, cancel := candidateContext(selectionCtx, len(candidates)-index)
		response, attemptErr := transport.exchangeCandidate(attemptCtx, candidate, wire, timeout)
		cancel()
		if attemptErr == nil {
			return response, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", candidate, attemptErr))
	}
	return nil, errors.Join(append([]error{ErrNoEndpoint}, failures...)...)
}

func (transport *Transport) exchangeCandidate(ctx context.Context, remote string, wire []byte, timeout time.Duration) ([]byte, error) {
	connection, err := transport.config.DialContext(ctx, transport.config.ProxyURL, remote, timeout)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		_ = connection.Close()
		return nil, transport.err()
	}
	transport.pending = connection
	transport.mu.Unlock()

	keep := false
	defer func() {
		transport.mu.Lock()
		if transport.pending == connection {
			transport.pending = nil
		}
		transport.mu.Unlock()
		if !keep {
			_ = connection.Close()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	if written, err := connection.Write(wire); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	} else if written != len(wire) {
		return nil, io.ErrShortWrite
	}
	buffer := make([]byte, maximumDatagramSize)
	for {
		n, err := connection.Read(buffer)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		if n == 0 || n == 1 && buffer[0] == 0xff {
			continue
		}
		packet := append([]byte(nil), buffer[:n]...)
		if hasNonESPMarker(packet) {
			packet = packet[4:]
		}
		if err := connection.SetDeadline(time.Time{}); err != nil {
			return nil, err
		}
		transport.mu.Lock()
		if transport.closed {
			transport.mu.Unlock()
			return nil, transport.err()
		}
		transport.pending = nil
		transport.conn = connection
		transport.selected = remote
		transport.mu.Unlock()
		keep = true
		go transport.readLoop(connection)
		return packet, nil
	}
}

func (transport *Transport) candidates(ctx context.Context) ([]string, time.Duration, error) {
	transport.mu.Lock()
	remote, timeout, closed := transport.remote, transport.timeout, transport.closed
	transport.mu.Unlock()
	if closed {
		return nil, 0, transport.err()
	}
	host, port, err := net.SplitHostPort(remote)
	if err != nil {
		return nil, 0, errors.New("outer UDP remote address must include a port")
	}
	host = strings.Trim(host, "[]")
	if address, err := netip.ParseAddr(host); err == nil {
		return []string{net.JoinHostPort(address.String(), port)}, timeout, nil
	}
	addresses, err := transport.config.ResolveContext(ctx, "ip", host)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve ePDG hostname through selected transport: %w", err)
	}
	addresses = usableAddresses(addresses)
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, net.JoinHostPort(address.String(), port))
	}
	if len(result) == 0 {
		return nil, 0, errors.New("ePDG hostname resolved without usable addresses")
	}
	if offset := int(transport.config.CandidateOffset % uint64(len(result))); offset > 0 {
		result = append(append([]string(nil), result[offset:]...), result[:offset]...)
	}
	return result, timeout, nil
}

func proxyResolveContext(proxyURL string, dial DialContextFunc) ResolveContextFunc {
	return func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip" || strings.TrimSpace(host) == "" {
			return nil, errors.New("proxy DNS lookup requires a non-empty IP hostname")
		}
		if ctx == nil {
			ctx = context.Background()
		}
		lookupCtx, cancel := context.WithTimeout(ctx, proxyDNSTimeout)
		defer cancel()
		type result struct {
			addresses []netip.Addr
			err       error
		}
		results := make(chan result, len(proxyDNSServers))
		for _, server := range proxyDNSServers {
			server := server
			go func() {
				resolver := &net.Resolver{
					PreferGo: true,
					Dial: func(dialCtx context.Context, dnsNetwork, _ string) (net.Conn, error) {
						if !strings.HasPrefix(dnsNetwork, "udp") {
							return nil, errors.New("proxy DNS TCP fallback is unsupported")
						}
						return dial(dialCtx, proxyURL, server, proxyDNSTimeout)
					},
				}
				addresses, err := resolver.LookupNetIP(lookupCtx, "ip", host)
				if err == nil {
					addresses = usableAddresses(addresses)
					if len(addresses) == 0 {
						err = errors.New("DNS response contained no usable addresses")
					}
				}
				results <- result{addresses: addresses, err: err}
			}()
		}
		failures := make([]error, 0, len(proxyDNSServers))
		for range proxyDNSServers {
			select {
			case resolved := <-results:
				if resolved.err == nil {
					cancel()
					return resolved.addresses, nil
				}
				failures = append(failures, resolved.err)
			case <-lookupCtx.Done():
				return nil, errors.Join(append(failures, lookupCtx.Err())...)
			}
		}
		return nil, errors.Join(failures...)
	}
}

func usableAddresses(addresses []netip.Addr) []netip.Addr {
	seen := make(map[netip.Addr]struct{}, len(addresses))
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() {
			continue
		}
		if _, found := seen[address]; found {
			continue
		}
		seen[address] = struct{}{}
		result = append(result, address)
	}
	return result
}

func candidateContext(ctx context.Context, remaining int) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok || remaining <= 1 {
		return context.WithCancel(ctx)
	}
	budget := time.Until(deadline) / time.Duration(remaining)
	if budget <= 0 {
		budget = time.Nanosecond
	}
	return context.WithTimeout(ctx, budget)
}

func (transport *Transport) err() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.errLocked()
}

func (transport *Transport) errLocked() error {
	if transport.failure != nil {
		return transport.failure
	}
	return ErrClosed
}

func (transport *Transport) deadline(ctx context.Context) time.Time {
	var deadline time.Time
	if ctx != nil {
		deadline, _ = ctx.Deadline()
	}
	transport.mu.Lock()
	timeout := transport.timeout
	transport.mu.Unlock()
	if timeout > 0 {
		candidate := time.Now().Add(timeout)
		if deadline.IsZero() || candidate.Before(deadline) {
			deadline = candidate
		}
	}
	return deadline
}

func hasNonESPMarker(packet []byte) bool {
	return len(packet) >= 4 && packet[0] == 0 && packet[1] == 0 && packet[2] == 0 && packet[3] == 0
}

func dialContext(ctx context.Context, proxyURL, remote string, timeout time.Duration) (net.Conn, error) {
	if proxyURL == "" {
		return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "udp", remote)
	}
	proxy, err := parseSOCKS5URL(proxyURL)
	if err != nil {
		return nil, err
	}
	username, password := "", ""
	if proxy.User != nil {
		username = proxy.User.Username()
		password, _ = proxy.User.Password()
	}
	client, err := socks5.NewClient(proxy.Host, username, password, 0, 0)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: timeout}
	client.DialTCP = func(network, _, address string) (net.Conn, error) {
		connection, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if deadline := connectionDeadline(ctx, timeout); !deadline.IsZero() {
			if err := connection.SetDeadline(deadline); err != nil {
				_ = connection.Close()
				return nil, err
			}
		}
		return connection, nil
	}
	if err := client.Negotiate(nil); err != nil {
		_ = client.Close()
		return nil, err
	}
	reply, err := client.Request(socks5.NewRequest(
		socks5.CmdUDP, socks5.ATYPIPv4,
		[]byte{0x00, 0x00, 0x00, 0x00}, []byte{0x00, 0x00},
	))
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	relay := reply.Address()
	host, port, err := net.SplitHostPort(relay)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && ip.IsUnspecified() {
		relay = net.JoinHostPort(proxy.Hostname(), port)
	}
	client.UDPConn, err = dialer.DialContext(ctx, "udp", relay)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	client.Dst = remote
	client.RemoteAddress = transportAddr(remote)
	_ = client.TCPConn.SetDeadline(time.Time{})
	_ = client.UDPConn.SetDeadline(time.Time{})
	return client, nil
}

func connectionDeadline(ctx context.Context, timeout time.Duration) time.Time {
	var deadline time.Time
	if ctx != nil {
		deadline, _ = ctx.Deadline()
	}
	if timeout > 0 {
		candidate := time.Now().Add(timeout)
		if deadline.IsZero() || candidate.Before(deadline) {
			deadline = candidate
		}
	}
	return deadline
}

type transportAddr string

func (address transportAddr) Network() string { return "udp" }
func (address transportAddr) String() string  { return string(address) }

func parseSOCKS5URL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "socks5" || parsed.Hostname() == "" || parsed.Port() == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("outer proxy must be an exact socks5 URL")
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("outer proxy port is invalid")
	}
	if parsed.User != nil {
		password, found := parsed.User.Password()
		if parsed.User.Username() == "" || !found || password == "" {
			return nil, errors.New("outer proxy credentials are incomplete")
		}
	}
	return parsed, nil
}
