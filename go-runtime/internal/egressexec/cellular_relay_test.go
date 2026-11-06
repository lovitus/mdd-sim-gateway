package egressexec

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"
)

// startRelayTestLease stands in for the Core lease listener: a SOCKS5 server
// that requires its own credentials and supports CONNECT and UDP ASSOCIATE.
func startRelayTestLease(t *testing.T, username, password string) (int, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := socks5.NewServer(socks5.WithCredential(socks5.StaticCredentials{username: password}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().(*net.TCPAddr).Port, func() { _ = listener.Close() }
}

func startRelayTestEcho(t *testing.T) (string, string) {
	t.Helper()
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tcpListener.Close() })
	go func() {
		for {
			conn, err := tcpListener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packetConn.Close() })
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, addr, err := packetConn.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = packetConn.WriteTo(buffer[:n], addr)
		}
	}()
	return tcpListener.Addr().String(), packetConn.LocalAddr().String()
}

// relaySocksClient is how sing-box reaches the exit: it sees only the relay.
func relaySocksClient(relay *cellularRelay, password string) *socks.Client {
	return socks.NewClient(N.SystemDialer, M.ParseSocksaddr(net.JoinHostPort("127.0.0.1", strconv.Itoa(relay.port))),
		socks.Version5, relay.username, password)
}

func relayEchoTCP(t *testing.T, client *socks.Client, target string) (net.Conn, error) {
	t.Helper()
	conn, err := client.DialContext(t.Context(), "tcp", M.ParseSocksaddr(target))
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, []byte("ping")) {
		_ = conn.Close()
		t.Fatalf("tcp echo through relay failed: reply=%q err=%v", reply, err)
	}
	return conn, nil
}

func TestCellularRelayCarriesTCPAndUDPOnlyThroughCurrentLease(t *testing.T) {
	tcpEcho, udpEcho := startRelayTestEcho(t)
	firstPort, closeFirst := startRelayTestLease(t, "lease-a", "pass-a")
	secondPort, _ := startRelayTestLease(t, "lease-b", "pass-b")
	client, err := newCellularClient("http://127.0.0.1:1", filepath.Join(t.TempDir(), "token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.close)
	relay, err := client.relayFor("sim-gb")
	if err != nil {
		t.Fatal(err)
	}
	relay.setUpstream(relayUpstreamFor(&cellularLease{ListenPort: firstPort, Username: "lease-a", Password: "pass-a"}))
	sing := relaySocksClient(relay, relay.password)

	held, err := relayEchoTCP(t, sing, tcpEcho)
	if err != nil {
		t.Fatalf("tcp through first lease: %v", err)
	}
	udp, err := sing.DialContext(t.Context(), "udp", M.ParseSocksaddr(udpEcho))
	if err != nil {
		t.Fatalf("udp associate through first lease: %v", err)
	}
	if _, err := udp.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if err := udp.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// The read buffer must hold the SOCKS datagram header as well as the payload.
	reply := make([]byte, 64)
	n, err := udp.Read(reply)
	if err != nil || string(reply[:n]) != "pong" {
		t.Fatalf("udp echo through first lease: reply=%q err=%v", reply[:n], err)
	}

	// Switching the lease closes flows bound to the old one, and new flows use the new one.
	relay.setUpstream(relayUpstreamFor(&cellularLease{ListenPort: secondPort, Username: "lease-b", Password: "pass-b"}))
	closeFirst()
	if err := held.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Read(make([]byte, 1)); err == nil {
		t.Fatal("flow on the superseded lease stayed open")
	}
	_ = held.Close()
	_ = udp.Close()
	again, err := relayEchoTCP(t, sing, tcpEcho)
	if err != nil {
		t.Fatalf("tcp through second lease: %v", err)
	}
	_ = again.Close()

	// No live lease: nothing reaches the host network directly, and UDP associate is refused too.
	relay.setUpstream(cellularRelayUpstream{})
	if _, err := relayEchoTCP(t, sing, tcpEcho); err == nil {
		t.Fatal("tcp was accepted without a live lease")
	}
	if _, err := sing.DialContext(t.Context(), "udp", M.ParseSocksaddr(udpEcho)); err == nil {
		t.Fatal("udp association was accepted without a live lease")
	}

	// The relay's own credentials still gate every client.
	if _, err := relaySocksClient(relay, "wrong-password").DialContext(t.Context(), "tcp", M.ParseSocksaddr(tcpEcho)); err == nil {
		t.Fatal("relay accepted the wrong password")
	}
}

func TestCellularRelayIdentitySurvivesExecutorRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "cellular-relays.json")
	first, err := newCellularClient("http://127.0.0.1:1", filepath.Join(t.TempDir(), "token"))
	if err != nil {
		t.Fatal(err)
	}
	first.relayStatePath = statePath
	original, err := first.relayFor("sim-gb")
	if err != nil {
		t.Fatal(err)
	}
	port, username, password := original.port, original.username, original.password
	first.close()

	second, err := newCellularClient("http://127.0.0.1:1", filepath.Join(t.TempDir(), "token"))
	if err != nil {
		t.Fatal(err)
	}
	second.relayStatePath = statePath
	t.Cleanup(second.close)
	restarted, err := second.relayFor("sim-gb")
	if err != nil {
		t.Fatal(err)
	}
	if restarted.port != port || restarted.username != username || restarted.password != password {
		t.Fatalf("relay identity changed across restart: %d/%s -> %d/%s", port, username, restarted.port, restarted.username)
	}
}

// A damaged or emptied state file must not panic the executor; the relay is rebuilt and recorded again.
func TestCellularRelayStateFileDamageRecoversAndPersists(t *testing.T) {
	for _, payload := range []string{"null", "{not json"} {
		t.Run(payload, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "cellular-relays.json")
			if err := os.WriteFile(statePath, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			client, err := newCellularClient("http://127.0.0.1:1", filepath.Join(t.TempDir(), "token"))
			if err != nil {
				t.Fatal(err)
			}
			client.relayStatePath = statePath
			t.Cleanup(client.close)
			relay, err := client.relayFor("sim-gb")
			if err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			var records map[string]cellularRelayRecord
			if err := json.Unmarshal(saved, &records); err != nil || records["sim-gb"].Port != relay.port {
				t.Fatalf("relay record not persisted after damaged state: %s err=%v", saved, err)
			}
		})
	}
}

// startCountingRelayTestLease is startRelayTestLease that also counts the UDP ASSOCIATE requests it serves.
func startCountingRelayTestLease(t *testing.T, username, password string) (int, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var associations atomic.Int64
	server := socks5.NewServer(socks5.WithCredential(socks5.StaticCredentials{username: password}),
		socks5.WithRule(relayTestAssociateCounter{count: &associations}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().(*net.TCPAddr).Port, &associations
}

type relayTestAssociateCounter struct{ count *atomic.Int64 }

func (counter relayTestAssociateCounter) Allow(ctx context.Context, request *socks5.Request) (context.Context, bool) {
	if request.Command == statute.CommandAssociate {
		counter.count.Add(1)
	}
	return ctx, true
}

// After a lease switch, the next datagram must leave through the new lease. The superseded
// lease stays alive, so a flow that survived the switch would echo through it and never reach the new one.
func TestCellularRelayUDPFlowMovesToCurrentLeaseAfterSwitch(t *testing.T) {
	_, udpEcho := startRelayTestEcho(t)
	firstPort, firstAssociations := startCountingRelayTestLease(t, "lease-a", "pass-a")
	secondPort, secondAssociations := startCountingRelayTestLease(t, "lease-b", "pass-b")
	client, err := newCellularClient("http://127.0.0.1:1", filepath.Join(t.TempDir(), "token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.close)
	relay, err := client.relayFor("sim-gb")
	if err != nil {
		t.Fatal(err)
	}
	relay.setUpstream(relayUpstreamFor(&cellularLease{ListenPort: firstPort, Username: "lease-a", Password: "pass-a"}))
	sing := relaySocksClient(relay, relay.password)
	udp, err := sing.DialContext(t.Context(), "udp", M.ParseSocksaddr(udpEcho))
	if err != nil {
		t.Fatalf("udp associate through first lease: %v", err)
	}
	defer udp.Close()
	reply := make([]byte, 64)
	// awaitEcho sends payload and reports whether that exact payload comes back before timeout.
	awaitEcho := func(payload string, timeout time.Duration) bool {
		if _, err := udp.Write([]byte(payload)); err != nil {
			return false
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if err := udp.SetReadDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			n, err := udp.Read(reply)
			if err != nil {
				return false
			}
			if string(reply[:n]) == payload {
				return true
			}
		}
		return false
	}
	if !awaitEcho("one", 5*time.Second) {
		t.Fatal("udp echo through first lease failed")
	}
	if firstAssociations.Load() != 1 {
		t.Fatalf("first datagram did not use the first lease: first=%d", firstAssociations.Load())
	}

	relay.setUpstream(relayUpstreamFor(&cellularLease{ListenPort: secondPort, Username: "lease-b", Password: "pass-b"}))
	// go-socks5 removes the superseded flow asynchronously, so probes may still echo through the
	// old lease until the new one serves its first association.
	for switchDeadline := time.Now().Add(10 * time.Second); secondAssociations.Load() == 0; {
		if time.Now().After(switchDeadline) {
			t.Fatalf("datagram after the lease switch never reached the new lease: first=%d", firstAssociations.Load())
		}
		_ = awaitEcho("probe", 100*time.Millisecond)
	}
	if !awaitEcho("marker", 5*time.Second) {
		t.Fatal("datagram after the lease switch was not relayed by the new lease")
	}
	if firstAssociations.Load() != 1 {
		t.Fatalf("superseded lease served a new association after the switch: first=%d second=%d", firstAssociations.Load(), secondAssociations.Load())
	}
}

// A busy identity port must not change the relay's credentials, and the new port is reused after restart.
func TestCellularRelayRebindsWhenIdentityPortIsBusy(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "cellular-relays.json")
	newClient := func(t *testing.T) *cellularClient {
		t.Helper()
		client, err := newCellularClient("http://127.0.0.1:1", filepath.Join(t.TempDir(), "token"))
		if err != nil {
			t.Fatal(err)
		}
		client.relayStatePath = statePath
		return client
	}
	first := newClient(t)
	original, err := first.relayFor("sim-gb")
	if err != nil {
		t.Fatal(err)
	}
	port, username, password := original.port, original.username, original.password
	first.close()

	busy, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	second := newClient(t)
	moved, err := second.relayFor("sim-gb")
	if err != nil {
		_ = busy.Close()
		t.Fatal(err)
	}
	if moved.port == port || moved.username != username || moved.password != password {
		_ = busy.Close()
		t.Fatalf("busy identity port was not replaced with credentials kept: %d -> %d", port, moved.port)
	}
	movedPort := moved.port
	second.close()
	_ = busy.Close()

	third := newClient(t)
	t.Cleanup(third.close)
	reused, err := third.relayFor("sim-gb")
	if err != nil {
		t.Fatal(err)
	}
	if reused.port != movedPort || reused.username != username || reused.password != password {
		t.Fatalf("replacement port was not reused after restart: want %d got %d", movedPort, reused.port)
	}
}
