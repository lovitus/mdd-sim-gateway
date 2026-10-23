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
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// Only MM/AT and the USB-parent sysfs fixture stand in for hardware. DHCP,
// nftables, cgroup, routing, sockets, policy persistence and WebSocket are real.
// This file also compiles against the pre-DHCP source for the one-time red proof.
type dhcpFixtureMM struct {
	mu          sync.Mutex
	snapshot    modemSnapshot
	epoch       string
	idle        bool
	disconnects int
	method      uint32
}

func (m *dhcpFixtureMM) Inventory(context.Context) ([]modemSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.snapshot
	s.Bearers = append([]dbus.ObjectPath(nil), s.Bearers...)
	s.BearerStates = map[dbus.ObjectPath]bool{}
	for key, value := range m.snapshot.BearerStates {
		s.BearerStates[key] = value
	}
	return []modemSnapshot{s}, nil
}
func (m *dhcpFixtureMM) Inhibit(context.Context, string, bool) error { return nil }
func (m *dhcpFixtureMM) Close() error                                { return nil }
func (m *dhcpFixtureMM) VoiceIdle(context.Context, dbus.ObjectPath) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idle, nil
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
	if err == nil {
		m.snapshot.Connected = true
		m.snapshot.Bearers = []dbus.ObjectPath{path}
		m.snapshot.BearerStates = map[dbus.ObjectPath]bool{path: true}
	}
	return bearer, err
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
	physical string
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

type dhcpWireEvent struct{ Type, Source, Destination, XID, Reply, Mode string }

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
	prober.guard = &dhcpFixtureGuard{Guard: guard, physical: physical}
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
	dhcpE2EMode(t, "renew")
	if err := owner.SetPersistent(ctx, target, profile, true); err != nil {
		t.Fatalf("DHCP_CONNECT_REJECTED: %v", err)
	}
	t.Log("DHCP_CONNECT_ACCEPTED: real quarantined Ethernet DORA and private route")
	mainRoutes := dhcpE2ECommand(t, "ip", "-4", "route", "show", "table", "main")
	hostDNS, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
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
	borrow := func() net.Conn {
		cycle++
		session := fmt.Sprintf("session-%d", cycle)
		expires := time.Now().Add(time.Minute)
		request := agentlink.ModemDataRequest{OperationID: "prepare-" + session, Action: agentlink.ModemDataPrepare, SessionID: session, EquipmentID: target.EquipmentID, AttachmentID: target.AttachmentID, CardID: target.CardID, SIMSessionGeneration: target.SIMSessionGeneration, ExpiresAt: expires, MaxBytes: 1 << 20}
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
				connection.Close()
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
	recoverOwner()
	t.Log("absolute DHCP expiry closes existing socket with MM still connected; same-SIM new claim cannot revive it")
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

// The peer runs in a separate namespace/process, not in the Agent cgroup.
func TestLinuxDHCPPeer(t *testing.T) {
	if os.Getenv("MDD_DHCP_PEER") != "1" {
		t.Skip("hosted peer helper")
	}
	root := os.Getenv("MDD_DHCP_FIXTURE")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
		var replyType dhcpv4.MessageType
		switch request.MessageType() {
		case dhcpv4.MessageTypeDiscover:
			if mode != "silent-init" {
				replyType = dhcpv4.MessageTypeOffer
			}
		case dhcpv4.MessageTypeRequest:
			if request.ClientIPAddr.Equal(net.IPv4zero) {
				if mode != "silent-init" {
					replyType = dhcpv4.MessageTypeAck
				}
			} else if mode == "nak" {
				replyType = dhcpv4.MessageTypeNak
			} else if mode == "renew" || mode == "rebind" && control.Dst.Equal(net.IPv4bcast) {
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
			if mode == "malformed" {
				dhcpv4.WithGeneric(dhcpv4.OptionIPAddressLeaseTime, []byte{8})(reply)
			}
			destination := net.IPv4bcast
			if request.ClientIPAddr.Equal(net.ParseIP("198.18.0.2")) && !control.Dst.Equal(net.IPv4bcast) && replyType != dhcpv4.MessageTypeNak {
				destination = net.ParseIP("198.18.0.2")
			}
			if _, err := ip.WriteTo(reply.ToBytes(), &ipv4.ControlMessage{Src: net.ParseIP("198.18.0.1"), IfIndex: control.IfIndex}, &net.UDPAddr{IP: destination, Port: 68}); err != nil {
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
