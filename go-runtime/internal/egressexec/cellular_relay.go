package egressexec

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"sync"

	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"
)

// cellularRelay is the fixed loopback SOCKS5 endpoint that sing-box uses for a
// borrowed cellular exit. Lease renewals, restarts and outages only change the
// upstream pointer here, so the shared sing-box configuration stays unchanged.
// Requests without a live upstream are refused instead of falling back direct.
type cellularRelay struct {
	port     int
	username string
	password string
	listener net.Listener
	server   *socks5.Server

	mu       sync.Mutex
	upstream cellularRelayUpstream
	// generation changes with every upstream switch or close, so an in-flight
	// dial cannot register a flow bound to a superseded lease.
	generation uint64
	closed     bool
	nextID     uint64
	links      map[uint64]*relayTrackedConn
}

var errCellularRelayUnavailable = errors.New("cellular borrowing is unavailable")

type cellularRelayUpstream struct {
	Available bool
	Address   string
	Username  string
	Password  string
}

type relayTrackedConn struct {
	net.Conn
	relay      *cellularRelay
	id         uint64
	generation uint64
	datagram   bool
}

func (conn *relayTrackedConn) Close() error {
	conn.relay.forget(conn.id)
	return conn.Conn.Close()
}

// Write refuses traffic on a superseded flow. go-socks5 ends the whole UDP association on any
// write error, so a superseded datagram flow drops the datagram and closes itself instead; the
// next datagram re-dials through the current lease. A switch can also close the flow while a
// write is in progress, so a failed write that the switch superseded takes the same path.
func (conn *relayTrackedConn) Write(p []byte) (int, error) {
	if conn.relay.current(conn.generation) {
		n, err := conn.Conn.Write(p)
		if err == nil || conn.relay.current(conn.generation) {
			return n, err
		}
	}
	if conn.datagram {
		_ = conn.Close()
		return len(p), nil
	}
	return 0, errCellularRelayUnavailable
}

type relayKeepDomainResolver struct{}

// Domain names must reach the borrowed network's resolver, not the host resolver.
func (relayKeepDomainResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

func newCellularRelay(listener net.Listener, port int, username, password string) *cellularRelay {
	relay := &cellularRelay{port: port, username: username, password: password, listener: listener,
		links: map[uint64]*relayTrackedConn{}}
	relay.server = socks5.NewServer(
		socks5.WithCredential(socks5.StaticCredentials{username: password}),
		socks5.WithResolver(relayKeepDomainResolver{}),
		socks5.WithRule(relay),
		socks5.WithDial(relay.dial),
	)
	return relay
}

func (relay *cellularRelay) serve() {
	_ = relay.server.Serve(relay.listener)
}

// Allow only CONNECT and UDP ASSOCIATE, and only while an upstream is live.
// Other commands would reach the host network directly.
func (relay *cellularRelay) Allow(ctx context.Context, request *socks5.Request) (context.Context, bool) {
	if request.Command != statute.CommandConnect && request.Command != statute.CommandAssociate {
		return ctx, false
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return ctx, relay.upstream.Available && !relay.closed
}

// dial opens one TCP or UDP flow through the current lease. The flow is
// registered only if the upstream did not change while it was being opened.
func (relay *cellularRelay) dial(ctx context.Context, network, address string) (net.Conn, error) {
	relay.mu.Lock()
	upstream, generation := relay.upstream, relay.generation
	relay.mu.Unlock()
	if !upstream.Available {
		return nil, errCellularRelayUnavailable
	}
	client := socks.NewClient(N.SystemDialer, M.ParseSocksaddr(upstream.Address), socks.Version5,
		upstream.Username, upstream.Password)
	conn, err := client.DialContext(ctx, network, M.ParseSocksaddr(address))
	if err != nil {
		return nil, err
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.closed || relay.generation != generation {
		_ = conn.Close()
		return nil, errCellularRelayUnavailable
	}
	relay.nextID++
	tracked := &relayTrackedConn{Conn: conn, relay: relay, id: relay.nextID, generation: generation, datagram: network == "udp"}
	relay.links[tracked.id] = tracked
	return tracked, nil
}

func (relay *cellularRelay) current(generation uint64) bool {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return !relay.closed && relay.generation == generation
}

func (relay *cellularRelay) forget(id uint64) {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	delete(relay.links, id)
}

// setUpstream switches every live flow to the new upstream. Flows that were
// bound to the previous lease are closed rather than silently continuing.
func (relay *cellularRelay) setUpstream(next cellularRelayUpstream) {
	relay.mu.Lock()
	if relay.upstream == next {
		relay.mu.Unlock()
		return
	}
	relay.upstream = next
	relay.generation++
	victims := relay.takeLinksLocked()
	relay.mu.Unlock()
	closeRelayLinks(victims)
}

func (relay *cellularRelay) close() {
	relay.mu.Lock()
	relay.closed = true
	relay.upstream = cellularRelayUpstream{}
	relay.generation++
	victims := relay.takeLinksLocked()
	relay.mu.Unlock()
	_ = relay.listener.Close()
	closeRelayLinks(victims)
}

func (relay *cellularRelay) takeLinksLocked() []*relayTrackedConn {
	victims := make([]*relayTrackedConn, 0, len(relay.links))
	for id, link := range relay.links {
		victims = append(victims, link)
		delete(relay.links, id)
	}
	return victims
}

func closeRelayLinks(links []*relayTrackedConn) {
	for _, link := range links {
		_ = link.Conn.Close()
	}
}

// listenCellularRelay keeps the previous loopback port when it is still free,
// so a restarted executor can reuse the identity sing-box already runs with.
func listenCellularRelay(preferred int) (net.Listener, error) {
	if preferred > 0 {
		if listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(preferred))); err == nil {
			return listener, nil
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func newCellularRelayCredentials() (string, string, error) {
	name := make([]byte, 8)
	secret := make([]byte, 24)
	if _, err := rand.Read(name); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(secret); err != nil {
		return "", "", err
	}
	return "mdd-relay-" + hex.EncodeToString(name), base64.RawURLEncoding.EncodeToString(secret), nil
}
