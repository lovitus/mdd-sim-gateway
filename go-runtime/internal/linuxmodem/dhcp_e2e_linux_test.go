//go:build linux && mdd_linux_dhcp_e2e

package linuxmodem

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentconnection"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
	"github.com/mdlayher/packet"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// Only MM/AT and the USB-parent sysfs fixture stand in for hardware. DHCP,
// nftables, cgroup, routing, sockets, policy persistence and WebSocket are real.
// This file also compiles against the pre-DHCP source for the one-time red proof.
type dhcpFixtureMM struct {
	mu              sync.Mutex
	snapshot        modemSnapshot
	epoch           string
	idle            bool
	disconnects     int
	method          uint32
	connectMethod   uint32
	parseError      error
	inhibitFailures int
	inhibitCalls    int
	idleError       error
	connectedReads  int
	delayAt         int
	readEntered     chan struct{}
	releaseRead     chan struct{}
}

func (m *dhcpFixtureMM) Inventory(ctx context.Context) ([]modemSnapshot, error) {
	m.mu.Lock()
	s := m.snapshot
	s.Bearers = append([]dbus.ObjectPath(nil), s.Bearers...)
	s.BearerStates = map[dbus.ObjectPath]bool{}
	for key, value := range m.snapshot.BearerStates {
		s.BearerStates[key] = value
	}
	if s.Connected {
		m.connectedReads++
	}
	delay := s.Connected && m.delayAt != 0 && m.connectedReads == m.delayAt
	entered, release := m.readEntered, m.releaseRead
	m.mu.Unlock()
	if delay {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []modemSnapshot{s}, nil
}
func (m *dhcpFixtureMM) Inhibit(_ context.Context, _ string, inhibit bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inhibit {
		m.inhibitCalls++
		if m.inhibitFailures > 0 {
			m.inhibitFailures--
			return errors.New("fixture MM inhibit failure")
		}
	}
	return nil
}
func (m *dhcpFixtureMM) Close() error { return nil }
func (m *dhcpFixtureMM) VoiceIdle(context.Context, dbus.ObjectPath) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idle, m.idleError
}
func (m *dhcpFixtureMM) SIMEpoch(dbus.ObjectPath, dbus.ObjectPath) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch, true
}
func (m *dhcpFixtureMM) Connect(_ context.Context, _ dbus.ObjectPath, _ agentdata.Profile) (dataBearer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := dbus.ObjectPath("/org/freedesktop/ModemManager1/Bearer/7")
	properties := map[string]dbus.Variant{
		"Connected": dbus.MakeVariant(true), "Interface": dbus.MakeVariant("mddcell"),
		"Ip4Config": dbus.MakeVariant(map[string]dbus.Variant{
			"method": dbus.MakeVariant(m.method), "address": dbus.MakeVariant("198.18.0.2"),
			"prefix": dbus.MakeVariant(uint32(24)), "gateway": dbus.MakeVariant("198.18.0.1"),
		}),
	}
	bearer, err := parseDataBearer(path, properties)
	m.connectMethod, m.parseError, m.connectedReads = m.method, err, 0
	if err == nil {
		m.snapshot.Connected = true
		m.snapshot.Bearers = []dbus.ObjectPath{path}
		m.snapshot.BearerStates = map[dbus.ObjectPath]bool{path: true}
	}
	return bearer, err
}

func (m *dhcpFixtureMM) Command(ctx context.Context, _ dbus.ObjectPath, command string, duration time.Duration) ([]byte, error) {
	return (&dhcpFixturePort{}).Exchange(ctx, command, duration)
}
func (m *dhcpFixtureMM) SendText(context.Context, dbus.ObjectPath, string, string, func(context.Context) error) ([]int, error) {
	return nil, errors.New("SMS is excluded from the DHCP fixture")
}
func (m *dhcpFixtureMM) Disconnect(context.Context, dbus.ObjectPath) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.idle {
		return errors.New("fixture voice call is active")
	}
	m.disconnects++
	m.snapshot.Connected = false
	m.snapshot.BearerStates = nil
	return nil
}

type dhcpFixturePort struct{ acquisitionPort }

func (p *dhcpFixturePort) Exchange(ctx context.Context, command string, duration time.Duration) ([]byte, error) {
	switch command {
	case "AT+CFUN?":
		return []byte("+CFUN: 1\r\nOK"), nil
	case "AT+CREG?", "AT+CEREG?":
		return []byte("+CREG: 0,1\r\nOK"), nil
	case "AT+CIMI":
		return []byte("001010123456789\r\nOK"), nil
	case "AT+CGDCONT?":
		return []byte("OK"), nil
	}
	return p.acquisitionPort.Exchange(ctx, command, duration)
}

type dhcpFixtureGuard struct {
	*linuxdataguard.Guard
	physical   string
	mu         sync.Mutex
	lastPermit linuxdataguard.DataPermit
}

func (g *dhcpFixtureGuard) OpenDataPermit(ctx context.Context, owner string) (linuxdataguard.DataPermit, error) {
	permit, err := g.Guard.OpenDataPermit(ctx, owner)
	g.mu.Lock()
	g.lastPermit = permit
	g.mu.Unlock()
	return permit, err
}

func (g *dhcpFixtureGuard) VerifyProtected(ctx context.Context, physical string, ports []string) error {
	if physical != g.physical || len(ports) != 1 || ports[0] != "mddcell" {
		return errors.New("wrong fixture USB identity")
	}
	if err := g.Guard.VerifyContract(ctx); err != nil {
		return err
	}
	// Veth has no USB parent. All actual network isolation uses the original guard.
	return g.Guard.ProtectNetdev(ctx, ports[0])
}

type dhcpFixtureCoordinator struct{}

func (dhcpFixtureCoordinator) DoAuxiliary(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func dhcpE2ECommand(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", args[0], err, output)
	}
	return output
}
func dhcpE2EAwait(t *testing.T, duration time.Duration, reason string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal(reason)
		case <-ticker.C:
		}
	}
}
func dhcpE2EMode(t *testing.T, mode string) {
	t.Helper()
	file := filepath.Join(os.Getenv("MDD_DHCP_FIXTURE"), "mode")
	if err := os.WriteFile(file+".new", []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file+".new", file); err != nil {
		t.Fatal(err)
	}
}

type dhcpWireEvent struct{ Type, Source, Destination, XID, Reply, Mode, Server string }

func dhcpE2EEvents(t *testing.T) []dhcpWireEvent {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("MDD_DHCP_FIXTURE"), "wire.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var events []dhcpWireEvent
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var event dhcpWireEvent
		if len(line) > 0 && json.Unmarshal(line, &event) == nil {
			events = append(events, event)
		}
	}
	return events
}

func TestLinuxDHCPClaimLifecycle(t *testing.T) {
	if os.Getenv("MDD_DHCP_FIXTURE") == "" {
		t.Skip("dedicated hosted network-namespace qualification")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root required in disposable fixture")
	}
	ctx := context.Background()
	dhcpE2EAwait(t, 10*time.Second, "peer did not start", func() bool {
		_, err := os.Stat(filepath.Join(os.Getenv("MDD_DHCP_FIXTURE"), "peer-ready"))
		return err == nil
	})
	guard, err := linuxdataguard.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := serialDiscoveryFixture(t)
	physical := filepath.Join(root, "bus", "usb", "devices", "1-2")
	netRoot := filepath.Join(root, "class", "net", "mddcell")
	if err := os.MkdirAll(netRoot, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"type", "netdev_group"} {
		if err := os.Symlink(filepath.Join("/sys/class/net/mddcell", name), filepath.Join(netRoot, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(physical, filepath.Join(netRoot, "device")); err != nil {
		t.Fatal(err)
	}
	mm := &dhcpFixtureMM{idle: true, epoch: "1:1", method: 3, snapshot: modemSnapshot{
		UID: "fixture-usb", EquipmentID: "862547055201716", ATPorts: []string{"ttyUSB2"}, NetPorts: []string{"mddcell"},
		ObjectPath: "/org/freedesktop/ModemManager1/Modem/0", SIMPath: "/org/freedesktop/ModemManager1/SIM/0",
		SIMState: agentmodem.SIMReady, ICCID: "8985200000000000001",
	}}
	prober, err := newProber(mm, false, "/fixture/audio", root, func(agentat.Candidate) (agentat.Port, error) { return &dhcpFixturePort{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	fixtureGuard := &dhcpFixtureGuard{Guard: guard, physical: physical}
	prober.guard = fixtureGuard
	defer prober.Close()
	probe := func() []agentmodem.Fact {
		facts, err := prober.Probe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return facts
	}
	facts := probe()
	if len(facts) != 1 || facts[0].Condition != agentmodem.DeviceReady || facts[0].SIM.SessionGeneration == "" {
		t.Fatalf("fixture did not acquire exact AT owner: %+v", facts)
	}
	target := agentdata.Target{EquipmentID: facts[0].EquipmentID, AttachmentID: facts[0].AttachmentID, CardID: facts[0].SIM.ICCID, SIMSessionGeneration: facts[0].SIM.SessionGeneration}
	owner, err := agentconnection.New(prober)
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentpolicy.Open(filepath.Join(t.TempDir(), "policy.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wanted := agentpolicy.Default(target.EquipmentID, target.CardID)
	wanted.Desired = agentpolicy.Desired{CellularEnabled: true, ConnectionEnabled: true, RoamingEnabled: true}
	stored, err := store.PutExpected(wanted, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := agentpolicy.New(agentpolicy.Config{Store: store, Runtime: prober, Connection: owner, Coordinator: dhcpFixtureCoordinator{}, Recovery: recovery.Policy{Base: time.Millisecond, Cap: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	profile := agentdata.Profile{AllowRoaming: true}
	mainRoutes := dhcpE2ECommand(t, "ip", "-4", "route", "show", "table", "main")
	hostDNS, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	dhcpE2EMode(t, "bootstrap")
	prepared := make(chan error, 1)
	go func() { prepared <- owner.SetPersistent(ctx, target, profile, true) }()
	// Old source returns from the actual MM method=3 parser. No unrelated
	// fixture, guard or preparation failure qualifies as the pre-fix result.
	dhcpE2EAwait(t, 10*time.Second, "neither DHCP bootstrap nor exact old rejection", func() bool {
		select {
		case err := <-prepared:
			mm.mu.Lock()
			method, parseErr := mm.connectMethod, mm.parseError
			mm.mu.Unlock()
			if method == 3 && parseErr != nil && errors.Is(err, parseErr) {
				t.Fatalf("DHCP_CONNECT_REJECTED: method=3 parser=%v", parseErr)
			}
			t.Fatalf("unexpected early PrepareData result: %v", err)
		default:
		}
		for _, e := range dhcpE2EEvents(t) {
			if e.Mode == "bootstrap" && e.Type == "DISCOVER" {
				return true
			}
		}
		return false
	})
	// Supply only an explicit fixture route/source for the negative probes.
	// No cellular address or product permit exists during DORA.
	dhcpE2ECommand(t, "ip", "route", "add", "198.18.0.1/32", "dev", "mddcell", "src", "192.0.2.2")
	assertBlocked := func(mark string, outsider bool, forwarding bool) {
		counter := func() uint64 {
			var dump struct {
				NFTables []struct {
					Rule *struct {
						Chain string
						Expr  []map[string]json.RawMessage
					} `json:"rule"`
				} `json:"nftables"`
			}
			if err := json.Unmarshal(dhcpE2ECommand(t, "nft", "-j", "list", "table", "inet", "mdd_cellular_guard"), &dump); err != nil {
				t.Fatal(err)
			}
			var total uint64
			for _, item := range dump.NFTables {
				if item.Rule == nil {
					continue
				}
				dropped := false
				var packets uint64
				for _, expression := range item.Rule.Expr {
					if _, ok := expression["drop"]; ok {
						dropped = true
					}
					if raw, ok := expression["counter"]; ok {
						var c struct{ Packets uint64 }
						if json.Unmarshal(raw, &c) != nil {
							t.Fatal("invalid nft counter")
						}
						packets = c.Packets
					}
				}
				if dropped {
					total += packets
				}
			}
			return total
		}
		before := counter()
		args := []string{os.Args[0], "-test.run=^TestLinuxDHCPDeniedSocket$", "-test.v"}
		env := append(os.Environ(), "MDD_DHCP_DENIED=1", "MDD_DHCP_MARK="+mark, "GORACE=atexit_sleep_ms=0")
		command := exec.Command(args[0], args[1:]...)
		if outsider || forwarding {
			ns := os.Getenv("MDD_DHCP_CLIENT_NS")
			if forwarding {
				ns = os.Getenv("MDD_DHCP_SENDER_NS")
				env = append(env, "MDD_DHCP_FORWARD=1")
			}
			args = append([]string{"--quiet", "--wait", "--pipe", "--collect", "--unit=mdd-dhcp-outsider", "--setenv=GORACE=atexit_sleep_ms=0", "--setenv=MDD_DHCP_DENIED=1", "--setenv=MDD_DHCP_MARK=" + mark, "--setenv=MDD_DHCP_FORWARD=" + strconv.FormatBool(forwarding), "ip", "netns", "exec", ns}, args...)
			command = exec.Command("systemd-run", args...)
		}
		command.Env = env
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("guard probe: %v %s", err, output)
		}
		if counter() <= before {
			t.Fatal("negative probe did not reach the real nft drop rule")
		}
	}
	fixtureGuard.mu.Lock()
	bootstrapMark := fixtureGuard.lastPermit.Mark
	fixtureGuard.mu.Unlock()
	assertBlocked("0", false, false)
	assertBlocked(strconv.FormatUint(uint64(bootstrapMark^1), 10), false, false)
	assertBlocked(strconv.FormatUint(uint64(bootstrapMark), 10), true, false)
	assertBlocked("0", true, true)
	dhcpE2ECommand(t, "ip", "route", "del", "198.18.0.1/32", "dev", "mddcell")
	dhcpE2EMode(t, "renew")
	if err := <-prepared; err != nil {
		t.Fatal("DHCP preparation", err)
	}
	t.Log("DHCP_CONNECT_ACCEPTED: real quarantined Ethernet DORA and private route")
	prober.mu.Lock()
	permit := prober.data[target.EquipmentID].route.Permit
	prober.mu.Unlock()
	mark := strconv.FormatUint(uint64(permit.Mark), 10)
	dhcpE2ECommand(t, "ip", "route", "add", "198.18.0.1/32", "dev", "mddcell", "src", "192.0.2.2")
	assertBlocked(strconv.FormatUint(uint64(permit.Mark^1), 10), false, false)
	assertBlocked(mark, true, false)
	assertBlocked("0", true, true)
	dhcpE2ECommand(t, "ip", "route", "del", "198.18.0.1/32", "dev", "mddcell")
	const token = "0123456789abcdef0123456789abcdef"
	broker, err := agentdata.NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return token, nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	data, err := agentdata.NewManager(agentdata.Config{Context: ctx, ServerURL: server.URL, ServerToken: token, AgentID: "fixture-agent", ProcessGeneration: "fixture-process", HTTPClient: &http.Client{Timeout: 3 * time.Second}, Backend: owner, Coordinator: dhcpFixtureCoordinator{}, Admission: policy})
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := policy.BindCoordinator(data); err != nil {
		t.Fatal(err)
	}
	cycle := 0
	var oldStream net.Conn
	var oldUDP net.Conn
	leaseDuration, budget := time.Minute, uint64(1<<20)
	borrow := func() net.Conn {
		cycle++
		session := fmt.Sprintf("session-%d", cycle)
		expires := time.Now().Add(leaseDuration)
		request := agentlink.ModemDataRequest{OperationID: "prepare-" + session, Action: agentlink.ModemDataPrepare, SessionID: session, EquipmentID: target.EquipmentID, AttachmentID: target.AttachmentID, CardID: target.CardID, SIMSessionGeneration: target.SIMSessionGeneration, ExpiresAt: expires, MaxBytes: budget}
		if response := data.ExecuteModemData(ctx, request); response.Failure != nil {
			t.Fatalf("borrow: %+v", response)
		}
		var tcp net.Conn
		for _, network := range []string{"tcp", "udp"} {
			stream := session + "-" + network
			if err := broker.Reserve(agentdata.Reservation{AgentID: "fixture-agent", ProcessGeneration: "fixture-process", SessionID: session, StreamID: stream, StreamToken: token, Network: network, ExpiresAt: expires}); err != nil {
				t.Fatal(err)
			}
			request.OperationID, request.Action, request.StreamID, request.StreamToken, request.Network, request.Address = "open-"+stream, agentlink.ModemDataOpen, stream, token, network, "echo.test:19090"
			if response := data.ExecuteModemData(ctx, request); response.Failure != nil {
				t.Fatalf("open: %+v", response)
			}
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			connection, err := broker.Acquire(bounded, stream)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			connection.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := connection.Write([]byte("probe")); err != nil {
				t.Fatal(err)
			}
			want := []byte("198.18.0.2|probe")
			got := make([]byte, len(want))
			if _, err := io.ReadFull(connection, got); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s did not use DHCP source: %q %v", network, got, err)
			}
			connection.SetDeadline(time.Time{})
			if network == "tcp" {
				tcp = connection
			} else {
				oldUDP = connection
				t.Cleanup(func() { connection.Close() })
			}
		}
		return tcp
	}
	oldStream = borrow()
	defer oldStream.Close()
	// The ordinary default route really works; a marked DHCP socket must not use it.
	ordinary, err := net.DialTimeout("tcp", "203.0.113.9:19090", time.Second)
	if err != nil {
		t.Fatal("alternate default route is not usable", err)
	}
	ordinary.SetDeadline(time.Now().Add(time.Second))
	ordinary.Write([]byte("probe"))
	plain := make([]byte, len("192.0.2.2|probe"))
	_, err = io.ReadFull(ordinary, plain)
	ordinary.Close()
	if err != nil || string(plain) != "192.0.2.2|probe" {
		t.Fatalf("default control=%q %v", plain, err)
	}
	// Unmarked sockets bound to the cellular link stay blocked.
	hostDialer := net.Dialer{Timeout: 250 * time.Millisecond, Control: func(_, _ string, fd syscall.RawConn) error {
		var e error
		err := fd.Control(func(raw uintptr) {
			e = unix.SetsockoptString(int(raw), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, "mddcell")
		})
		return errors.Join(err, e)
	}}
	if connection, err := hostDialer.DialContext(ctx, "tcp4", "198.18.0.1:19090"); err == nil {
		connection.Close()
		t.Fatal("ordinary host traffic escaped cellular guard")
	}
	findEvent := func(mode, reply, destination string) *dhcpWireEvent {
		for _, event := range dhcpE2EEvents(t) {
			if event.Mode == mode && event.Reply == reply && event.Destination == destination && event.Source == "198.18.0.2:68" {
				return &event
			}
		}
		return nil
	}
	dhcpE2EAwait(t, 10*time.Second, "no real T1 unicast ACK", func() bool { return findEvent("renew", "ACK", "198.18.0.1") != nil })
	t1 := *findEvent("renew", "ACK", "198.18.0.1")
	dhcpE2EMode(t, "rebind")
	dhcpE2EAwait(t, 12*time.Second, "no real T2 broadcast ACK", func() bool { return findEvent("rebind", "ACK", "255.255.255.255") != nil })
	t2 := *findEvent("rebind", "ACK", "255.255.255.255")
	if t1.XID == t2.XID {
		t.Fatal("renew/rebind reused transaction identity")
	}
	if t2.Server != "198.18.0.3" {
		t.Fatal("T2 did not select the second server")
	}
	dhcpE2EMode(t, "renew-second")
	dhcpE2EAwait(t, 8*time.Second, "T1 did not adopt the second server", func() bool { return findEvent("renew-second", "ACK", "198.18.0.3") != nil })
	t.Log("T1 unicast and T2 broadcast renew the same active borrower without host fallback")
	checkClosed := func(connection net.Conn) {
		connection.SetReadDeadline(time.Now().Add(3 * time.Second))
		one := make([]byte, 1)
		_, err := connection.Read(one)
		if err == nil {
			t.Fatal("invalidated stream still readable")
		}
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatal("invalidated stream was not closed")
		}
	}
	recoverOwner := func() {
		dhcpE2EAwait(t, 5*time.Second, "borrower admission was not released", func() bool {
			return data.DoAuxiliary(ctx, target.EquipmentID, func(context.Context) error { return nil }) == nil
		})
		mm.mu.Lock()
		mm.idle = true
		mm.mu.Unlock()
		dhcpE2EMode(t, "renew")
		dhcpE2EAwait(t, 8*time.Second, "existing policy did not recover persistent owner", func() bool { policy.ReconcilePolicies(ctx, probe()); return owner.Persistent(target) })
		after, found, err := store.Get(target.EquipmentID, target.CardID)
		if err != nil || !found || after != stored {
			t.Fatal("recovery changed durable user policy")
		}
		checkClosed(oldStream)
		checkClosed(oldUDP)
	}
	mm.mu.Lock()
	mm.idle = false
	beforeDisconnect := mm.disconnects
	mm.mu.Unlock()
	dhcpE2EMode(t, "nak")
	dhcpE2EAwait(t, 10*time.Second, "NAK was not delivered", func() bool {
		for _, e := range dhcpE2EEvents(t) {
			if e.Mode == "nak" && e.Reply == "NAK" {
				return true
			}
		}
		return false
	})
	checkClosed(oldStream)
	checkClosed(oldUDP)
	policy.ReconcilePolicies(ctx, probe())
	mm.mu.Lock()
	disconnected := mm.disconnects
	mm.mu.Unlock()
	if disconnected != beforeDisconnect || owner.Persistent(target) {
		t.Fatal("NAK bypassed call guard or retained readiness")
	}
	recoverOwner()
	t.Log("NAK closes broker stream immediately; busy call prevents destructive cleanup; original policy recovers")
	oldStream = borrow()
	defer oldStream.Close()
	dhcpE2EMode(t, "silent")
	checkDeadline := time.Now().Add(13 * time.Second)
	oldStream.SetReadDeadline(checkDeadline)
	var one [1]byte
	_, err = oldStream.Read(one[:])
	if err == nil {
		t.Fatal("expired stream remained open")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("DHCP expiry did not close stream")
	}
	if owner.Persistent(target) {
		t.Fatal("MM connected restored expired DHCP readiness")
	}
	checkClosed(oldUDP)
	recoverOwner()
	t.Log("absolute DHCP expiry closes existing socket with MM still connected; same-SIM new claim cannot revive it")
	oldStream = borrow()
	defer oldStream.Close()
	dhcpE2EMode(t, "changed")
	dhcpE2EAwait(t, 10*time.Second, "changed DHCP DNS did not invalidate owner", func() bool { return !owner.Persistent(target) })
	checkClosed(oldStream)
	checkClosed(oldUDP)
	recoverOwner()

	// User lease expiry and quota remain independent of DHCP renewal.
	leaseDuration = 6 * time.Second
	oldStream = borrow()
	oldStream.SetReadDeadline(time.Now().Add(9 * time.Second))
	if _, err := oldStream.Read(one[:]); err == nil {
		t.Fatal("user lease was extended by DHCP")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("user lease did not expire")
	}
	checkClosed(oldUDP)
	if !owner.Persistent(target) {
		t.Fatal("user lease expiry stopped persistent 4G")
	}
	dhcpE2EAwait(t, 5*time.Second, "expired borrower retained admission", func() bool {
		return data.DoAuxiliary(ctx, target.EquipmentID, func(context.Context) error { return nil }) == nil
	})
	leaseDuration, budget = time.Minute, 50
	oldStream = borrow()
	defer oldStream.Close()
	beforeRenewals := len(dhcpE2EEvents(t))
	dhcpE2EAwait(t, 6*time.Second, "quota scenario did not cross DHCP renewal", func() bool {
		for _, e := range dhcpE2EEvents(t)[beforeRenewals:] {
			if e.Reply == "ACK" && e.Source == "198.18.0.2:68" {
				return true
			}
		}
		return false
	})
	if _, err := oldStream.Write([]byte("probe")); err != nil {
		t.Fatal(err)
	}
	checkClosed(oldStream)
	checkClosed(oldUDP)
	if !owner.Persistent(target) {
		t.Fatal("quota exhaustion stopped persistent 4G")
	}
	budget = 1 << 20
	dhcpE2EAwait(t, 5*time.Second, "quota borrower retained admission", func() bool {
		return data.DoAuxiliary(ctx, target.EquipmentID, func(context.Context) error { return nil }) == nil
	})

	// No borrower: retain the cleanup owner through partial MM failure, then
	// recheck voice state before the still-pending Inhibit transaction.
	mm.mu.Lock()
	mm.inhibitFailures = 1
	beforeDisconnect = mm.disconnects
	mm.mu.Unlock()
	if err := owner.ReleaseStale(ctx, target); err == nil {
		t.Fatal("inhibit failure was lost")
	}
	mm.mu.Lock()
	attempts := mm.inhibitCalls
	disconnected = mm.disconnects
	mm.idle = false
	mm.mu.Unlock()
	if disconnected != beforeDisconnect+1 {
		t.Fatal("partial cleanup did not disconnect exactly once")
	}
	for _, unknown := range []bool{false, true} {
		mm.mu.Lock()
		if unknown {
			mm.idleError = errors.New("fixture voice state unknown")
		}
		mm.mu.Unlock()
		if err := owner.ReleaseStale(ctx, target); err == nil {
			t.Fatal("busy/unknown cleanup was accepted")
		}
		mm.mu.Lock()
		calls := mm.inhibitCalls
		mm.mu.Unlock()
		if calls != attempts {
			t.Fatal("cleanup retried Inhibit without current idle authority")
		}
	}
	mm.mu.Lock()
	mm.idle = true
	mm.idleError = nil
	mm.mu.Unlock()
	if err := owner.ReleaseStale(ctx, target); err != nil {
		t.Fatal(err)
	}
	recoverOwner()
	dhcpE2EMode(t, "silent")
	dhcpE2EAwait(t, 12*time.Second, "persistent-only lease did not expire", func() bool { return !owner.Persistent(target) })
	recoverOwner()
	t.Log("changed DNS, user expiry/quota, partial MM cleanup and persistent-only recovery passed")

	// Guard routing must not fall back even while a valid alternate default
	// remains reachable. Check the actual marked data entry after each removal.
	if _, err := owner.PrepareData(ctx, target, profile); err != nil {
		t.Fatal(err)
	}
	defaultEvidence := filepath.Join(os.Getenv("MDD_DHCP_FIXTURE"), "default-packets")
	defaultBefore, err := os.ReadFile(defaultEvidence)
	if err != nil || len(defaultBefore) == 0 {
		t.Fatal("actual alternate-path packets were not captured", err)
	}
	prober.mu.Lock()
	permit = prober.data[target.EquipmentID].route.Permit
	table := prober.data[target.EquipmentID].route.Table
	prober.mu.Unlock()
	for _, revoke := range []bool{false, true} {
		if revoke {
			if err := guard.CloseDataPermit(ctx, permit); err != nil {
				t.Fatal(err)
			}
		} else {
			dhcpE2ECommand(t, "ip", "route", "del", "table", strconv.FormatUint(uint64(table), 10), "default")
		}
		for _, address := range []string{"203.0.113.9:19090", "echo.test:19090"} {
			bounded, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
			c, err := owner.DialData(bounded, target, "tcp", address)
			cancel()
			if err == nil {
				c.Close()
				t.Fatal("bound data/DNS fell back after route/permit withdrawal")
			}
		}
	}
	if after, err := os.ReadFile(defaultEvidence); err != nil || !bytes.Equal(defaultBefore, after) {
		t.Fatal("bound data/DNS emitted a packet on the alternate default path", err)
	}
	if err := owner.StopData(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := owner.ReleaseStale(ctx, target); err != nil {
		t.Fatal(err)
	}

	// A three-second real lease expires during hardware readback both before
	// and after permit publication. No successful preparation may survive it.
	for _, readAt := range []int{3, 5} {
		dhcpE2EMode(t, "short-lease")
		mm.mu.Lock()
		mm.delayAt = readAt
		mm.readEntered = make(chan struct{})
		mm.releaseRead = make(chan struct{})
		entered, release := mm.readEntered, mm.releaseRead
		mm.mu.Unlock()
		done := make(chan error, 1)
		go func() { _, err := prober.PrepareData(ctx, target, profile); done <- err }()
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("short-lease did not reach selected readback: %v", err)
		case <-time.After(8 * time.Second):
			t.Fatal("readback barrier not entered")
		}
		if readAt == 5 && !bytes.Contains(dhcpE2ECommand(t, "nft", "-j", "list", "table", "inet", "mdd_cellular_guard"), []byte("mdd-agent.service")) {
			t.Fatal("post-publication readback never had a real permit")
		}
		<-time.After(3500 * time.Millisecond)
		// This observes the kernel while readback is stalled, not just the
		// eventual return or a mock permit flag.
		nft := dhcpE2ECommand(t, "nft", "-j", "list", "table", "inet", "mdd_cellular_guard")
		if bytes.Contains(nft, []byte("mdd-agent.service")) {
			t.Fatal("expired preparation retained published socket permit")
		}
		close(release)
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("expired preparation succeeded")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("expired preparation did not finish cleanup")
		}
		if err := prober.StopData(ctx, target); err != nil {
			t.Fatal(err)
		}
		address := dhcpE2ECommand(t, "ip", "-4", "-j", "addr", "show", "dev", "mddcell")
		var links []struct {
			Addresses []json.RawMessage `json:"addr_info"`
		}
		if json.Unmarshal(address, &links) != nil || len(links) != 1 || len(links[0].Addresses) != 0 {
			t.Fatal("expired preparation retained address")
		}
		mm.mu.Lock()
		mm.delayAt = 0
		mm.mu.Unlock()
	}
	recoverOwner()
	if got := dhcpE2ECommand(t, "ip", "-4", "route", "show", "table", "main"); !bytes.Equal(got, mainRoutes) {
		t.Fatalf("host main route changed: %s", got)
	}
	if got, err := os.ReadFile("/etc/resolv.conf"); err != nil || !bytes.Equal(got, hostDNS) {
		t.Fatal("host DNS changed")
	}
	// A replaced SIM event generation invalidates the exact existing claim.
	oldStream = borrow()
	defer oldStream.Close()
	mm.mu.Lock()
	mm.epoch = "1:2"
	mm.mu.Unlock()
	checkClosed(oldStream)
	if owner.Persistent(target) {
		t.Fatal("replaced SIM retained old claim")
	}
	if err := owner.StopData(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := owner.ReleaseStale(ctx, target); err != nil {
		t.Fatal(err)
	}
	facts = probe()
	target.SIMSessionGeneration = facts[0].SIM.SessionGeneration
	// Stop before ACK must cancel its only DHCP FD and retain normal cleanup.
	dhcpE2EMode(t, "silent-init")
	bounded, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	_, err = prober.PrepareData(bounded, target, profile)
	cancel()
	if err == nil {
		t.Fatal("cancelled DORA acquired a connection")
	}
	if err := prober.StopData(ctx, target); err != nil {
		t.Fatal(err)
	}
	dhcpE2EMode(t, "malformed")
	if _, err := prober.PrepareData(ctx, target, profile); err == nil {
		t.Fatal("malformed DHCP lease was published")
	}
	if err := prober.StopData(ctx, target); err != nil {
		t.Fatal(err)
	}
	// Static MM remains a supported path with no DHCP exchange.
	mm.mu.Lock()
	mm.method = 2
	mm.mu.Unlock()
	if err := owner.SetPersistent(ctx, target, profile, true); err != nil {
		t.Fatal("static regression", err)
	}
	if err := owner.SetPersistent(ctx, target, profile, false); err != nil {
		t.Fatal(err)
	}
	mm.mu.Lock()
	mm.method = 3
	mm.mu.Unlock()
	dhcpE2EMode(t, "renew")
	if err := owner.SetPersistent(ctx, target, profile, true); err != nil {
		t.Fatal(err)
	}
	_, err = owner.PrepareData(ctx, target, profile)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := owner.DialData(ctx, target, "tcp", "203.0.113.9:19090")
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	dhcpE2ECommand(t, "ip", "link", "del", "mddcell")
	dhcpE2ECommand(t, "ip", "link", "add", "mddcell", "type", "dummy")
	dhcpE2ECommand(t, "ip", "link", "set", "mddcell", "up")
	dhcpE2ECommand(t, "ip", "addr", "add", "203.0.113.200/32", "dev", "mddcell")
	checkClosed(connection)
	if err := owner.StopData(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := owner.ReleaseStale(ctx, target); err != nil {
		t.Fatal(err)
	}
	link, err := net.InterfaceByName("mddcell")
	if err != nil || link.Flags&net.FlagUp == 0 {
		t.Fatal("late cleanup downed same-name replacement")
	}
	addresses, err := link.Addrs()
	if err != nil || len(addresses) == 0 {
		t.Fatal("late cleanup removed replacement address")
	}
	t.Log("cancellation, static compatibility, SIM epoch and same-name replacement fences passed")
}

// Separate process used in both the Agent's own and a different real cgroup.
func TestLinuxDHCPDeniedSocket(t *testing.T) {
	if os.Getenv("MDD_DHCP_DENIED") != "1" {
		t.Skip("hosted negative socket probe")
	}
	mark, err := strconv.ParseUint(os.Getenv("MDD_DHCP_MARK"), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	forwarding := os.Getenv("MDD_DHCP_FORWARD") == "1" || os.Getenv("MDD_DHCP_FORWARD") == "true"
	dialer := net.Dialer{Timeout: 400 * time.Millisecond}
	if !forwarding {
		dialer.LocalAddr = &net.TCPAddr{IP: net.ParseIP("192.0.2.2")}
		dialer.Control = func(_, _ string, raw syscall.RawConn) error {
			var failure error
			err := raw.Control(func(fd uintptr) {
				failure = errors.Join(unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, "mddcell"), unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark)))
			})
			return errors.Join(err, failure)
		}
	}
	c, err := dialer.Dial("tcp4", "198.18.0.1:19090")
	if err == nil {
		c.Close()
		t.Fatal("unauthorized cellular socket connected")
	}
	if failure, ok := err.(net.Error); !ok || !failure.Timeout() {
		t.Fatalf("probe failed before packet denial: %v", err)
	}
}

// The peer runs in a separate namespace/process, not in the Agent cgroup.
func TestLinuxDHCPPeer(t *testing.T) {
	if os.Getenv("MDD_DHCP_PEER") != "1" {
		t.Skip("hosted peer helper")
	}
	root := os.Getenv("MDD_DHCP_FIXTURE")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	lc := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var e error
		err := raw.Control(func(fd uintptr) {
			e = errors.Join(unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, "cell-peer"), unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1))
		})
		return errors.Join(err, e)
	}}
	udp, err := lc.ListenPacket(ctx, "udp4", ":67")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	ip := ipv4.NewPacketConn(udp)
	if err := ip.SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", ":19090")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	echo, err := net.ListenPacket("udp4", ":19090")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	dns, err := net.ListenPacket("udp4", "198.18.0.1:53")
	if err != nil {
		t.Fatal(err)
	}
	defer dns.Close()
	defaultInterface, err := net.InterfaceByName("default-peer")
	if err != nil {
		t.Fatal(err)
	}
	defaultPackets, err := packet.Listen(defaultInterface, packet.Datagram, unix.ETH_P_IP, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer defaultPackets.Close()
	capture, err := os.OpenFile(filepath.Join(root, "default-packets"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, _, err := defaultPackets.ReadFrom(buffer)
			if err != nil {
				return
			}
			if n < 24 || buffer[0]>>4 != 4 || !bytes.Equal(buffer[12:16], net.ParseIP("192.0.2.2").To4()) {
				continue
			}
			header := int(buffer[0]&15) * 4
			if header < 20 || n < header+4 || (buffer[9] != 6 && buffer[9] != 17) {
				continue
			}
			port := binary.BigEndian.Uint16(buffer[header+2 : header+4])
			if port == 53 || port == 19090 {
				if _, err := capture.Write([]byte{1}); err != nil {
					return
				}
			}
		}
	}()
	go func() {
		buffer := make([]byte, 1500)
		for {
			n, from, err := dns.ReadFrom(buffer)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if query.Unpack(buffer[:n]) != nil {
				continue
			}
			answer := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true}, Questions: query.Questions}
			for _, question := range query.Questions {
				if question.Name.String() == "echo.test." && question.Type == dnsmessage.TypeA {
					answer.Answers = append(answer.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.AResource{A: [4]byte{203, 0, 113, 9}}})
				}
			}
			payload, err := answer.Pack()
			if err == nil {
				dns.WriteTo(payload, from)
			}
		}
	}()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buffer := make([]byte, 256)
				for {
					n, err := c.Read(buffer)
					if err != nil {
						return
					}
					host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
					if _, err = c.Write(append([]byte(host+"|"), buffer[:n]...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	go func() {
		buffer := make([]byte, 256)
		for {
			n, from, err := echo.ReadFrom(buffer)
			if err != nil {
				return
			}
			host, _, _ := net.SplitHostPort(from.String())
			echo.WriteTo(append([]byte(host+"|"), buffer[:n]...), from)
		}
	}()
	go func() { <-ctx.Done(); udp.Close(); listener.Close(); echo.Close() }()
	if err := os.WriteFile(filepath.Join(root, "peer-ready"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		buffer := make([]byte, 2048)
		n, control, from, err := ip.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			t.Fatal(err)
		}
		request, err := dhcpv4.FromBytes(buffer[:n])
		if err != nil {
			continue
		}
		modeBytes, err := os.ReadFile(filepath.Join(root, "mode"))
		if err != nil {
			t.Fatal(err)
		}
		mode := string(modeBytes)
		event := dhcpWireEvent{Type: request.MessageType().String(), Source: from.String(), Destination: control.Dst.String(), XID: fmt.Sprintf("%x", request.TransactionID), Mode: mode}
		server := "198.18.0.1"
		if mode == "rebind" || mode == "renew-second" || control.Dst.Equal(net.ParseIP("198.18.0.3")) {
			server = "198.18.0.3"
		}
		event.Server = server
		var replyType dhcpv4.MessageType
		switch request.MessageType() {
		case dhcpv4.MessageTypeDiscover:
			if mode != "silent-init" && mode != "bootstrap" {
				replyType = dhcpv4.MessageTypeOffer
			}
		case dhcpv4.MessageTypeRequest:
			if request.ClientIPAddr.Equal(net.IPv4zero) {
				if mode != "silent-init" {
					replyType = dhcpv4.MessageTypeAck
				}
			} else if mode == "nak" {
				replyType = dhcpv4.MessageTypeNak
			} else if mode == "renew" || mode == "renew-second" || mode == "changed" || mode == "rebind" && control.Dst.Equal(net.IPv4bcast) {
				replyType = dhcpv4.MessageTypeAck
			}
		}
		if replyType != 0 {
			event.Reply = replyType.String()
			seconds := func(value uint32) []byte { raw := make([]byte, 4); binary.BigEndian.PutUint32(raw, value); return raw }
			reply, err := dhcpv4.New(dhcpv4.WithReply(request), dhcpv4.WithYourIP(net.ParseIP("198.18.0.2")), dhcpv4.WithMessageType(replyType), dhcpv4.WithServerIP(net.ParseIP("198.18.0.1")), dhcpv4.WithGeneric(dhcpv4.OptionServerIdentifier, net.ParseIP("198.18.0.1").To4()), dhcpv4.WithNetmask(net.CIDRMask(24, 32)), dhcpv4.WithGeneric(dhcpv4.OptionRouter, net.ParseIP("198.18.0.1").To4()), dhcpv4.WithGeneric(dhcpv4.OptionDomainNameServer, net.ParseIP("198.18.0.1").To4()), dhcpv4.WithLeaseTime(8), dhcpv4.WithGeneric(dhcpv4.OptionRenewTimeValue, seconds(3)), dhcpv4.WithGeneric(dhcpv4.OptionRebindingTimeValue, seconds(5)))
			if err != nil {
				t.Fatal(err)
			}
			dhcpv4.WithGeneric(dhcpv4.OptionServerIdentifier, net.ParseIP(server).To4())(reply)
			if mode == "changed" && !request.ClientIPAddr.Equal(net.IPv4zero) {
				dhcpv4.WithGeneric(dhcpv4.OptionDomainNameServer, net.ParseIP("198.18.0.3").To4())(reply)
			}
			if mode == "short-lease" {
				dhcpv4.WithLeaseTime(3)(reply)
				dhcpv4.WithGeneric(dhcpv4.OptionRenewTimeValue, seconds(1))(reply)
				dhcpv4.WithGeneric(dhcpv4.OptionRebindingTimeValue, seconds(2))(reply)
			}
			if mode == "malformed" {
				dhcpv4.WithGeneric(dhcpv4.OptionIPAddressLeaseTime, []byte{8})(reply)
			}
			destination := net.IPv4bcast
			if request.ClientIPAddr.Equal(net.ParseIP("198.18.0.2")) && !control.Dst.Equal(net.IPv4bcast) && replyType != dhcpv4.MessageTypeNak {
				destination = net.ParseIP("198.18.0.2")
			}
			if mode == "renew-second" {
				// An ACK from the superseded server cannot extend the selected
				// server's lease. Follow it with the legitimate response.
				stale := *reply
				stale.Options = make(dhcpv4.Options)
				for k, v := range reply.Options {
					stale.Options[k] = append([]byte(nil), v...)
				}
				dhcpv4.WithGeneric(dhcpv4.OptionServerIdentifier, net.ParseIP("198.18.0.1").To4())(&stale)
				dhcpv4.WithGeneric(dhcpv4.OptionDomainNameServer, net.ParseIP("198.18.0.99").To4())(&stale)
				if _, err := ip.WriteTo(stale.ToBytes(), &ipv4.ControlMessage{Src: net.ParseIP("198.18.0.1"), IfIndex: control.IfIndex}, &net.UDPAddr{IP: destination, Port: 68}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ip.WriteTo(reply.ToBytes(), &ipv4.ControlMessage{Src: net.ParseIP(server), IfIndex: control.IfIndex}, &net.UDPAddr{IP: destination, Port: 68}); err != nil {
				t.Fatal(err)
			}
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(root, "wire.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = file.Write(append(encoded, '\n'))
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(errors.Join(err, closeErr))
		}
	}
}
