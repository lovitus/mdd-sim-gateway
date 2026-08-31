//go:build linux || windows

package rawusb

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func TestMultiplexCarriesManyUSBIPStreamsOverOneConnection(t *testing.T) {
	physicalClient, physicalServer := net.Pipe()
	exporter := &Exporter{listener: newConnListener()}
	serverDone := make(chan error, 1)
	go func() { serverDone <- exporter.ServeMultiplexed(context.Background(), physicalServer) }()
	dialer := &singleConnDialer{conn: physicalClient}
	client, err := newMuxClientWithDialer(dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	for index := 0; index < 3; index++ {
		dialContext, cancelDial := context.WithTimeout(context.Background(), 2*time.Second)
		logicalClient, err := client.DialContext(dialContext, N.NetworkTCP,
			M.ParseSocksaddrHostPort("usbip.mdd.internal", 3240))
		cancelDial()
		if err != nil {
			t.Fatal(err)
		}
		if err := logicalClient.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatalf("stream %d does not provide the sing-usbip deadline contract: %v", index, err)
		}
		want := []byte{byte(index + 1), 0x55, 0xaa}
		go func(payload []byte) { _, _ = logicalClient.Write(payload) }(want)
		accepted := make(chan acceptResult, 1)
		go func() {
			conn, acceptErr := exporter.listener.Accept()
			accepted <- acceptResult{conn: conn, err: acceptErr}
		}()
		var logicalServer net.Conn
		select {
		case result := <-accepted:
			if result.err != nil {
				t.Fatal(result.err)
			}
			logicalServer = result.conn
		case <-time.After(2 * time.Second):
			t.Fatal("multiplex client did not open its logical USB/IP stream")
		}
		got := make([]byte, len(want))
		if _, err := io.ReadFull(logicalServer, got); err != nil || string(got) != string(want) {
			t.Fatalf("stream %d got=%x err=%v", index, got, err)
		}
		_ = logicalClient.Close()
		_ = logicalServer.Close()
	}
	if dials := dialer.dialCount(); dials != 1 {
		t.Fatalf("three logical streams used %d underlying WSS connections", dials)
	}
	_ = client.Close()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("multiplex server did not stop with its one physical connection")
	}
}

func TestYamuxDeadlinesAndCloseArePerLogicalStream(t *testing.T) {
	physicalClient, physicalServer := net.Pipe()
	exporter := &Exporter{listener: newConnListener()}
	serverDone := make(chan error, 1)
	go func() { serverDone <- exporter.ServeMultiplexed(context.Background(), physicalServer) }()
	dialer := &singleConnDialer{conn: physicalClient}
	client, err := newMuxClientWithDialer(dialer)
	if err != nil {
		t.Fatal(err)
	}

	clientA := dialMuxStream(t, client)
	go func() { _, _ = clientA.Write([]byte{0xa1}) }()
	serverA := acceptMuxStream(t, exporter.listener)
	if payload := readMuxPayload(t, serverA, 1); payload[0] != 0xa1 {
		t.Fatalf("stream A establishment payload=%x", payload)
	}
	clientB := dialMuxStream(t, client)
	go func() { _, _ = clientB.Write([]byte{0xb1}) }()
	serverB := acceptMuxStream(t, exporter.listener)
	if payload := readMuxPayload(t, serverB, 1); payload[0] != 0xb1 {
		t.Fatalf("stream B establishment payload=%x", payload)
	}
	defer clientB.Close()
	defer serverB.Close()

	for name, conn := range map[string]net.Conn{"client A": clientA, "server A": serverA, "client B": clientB, "server B": serverB} {
		if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatalf("%s SetDeadline: %v", name, err)
		}
		if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatalf("%s SetWriteDeadline: %v", name, err)
		}
		if err := conn.SetDeadline(time.Time{}); err != nil {
			t.Fatalf("%s clear deadline: %v", name, err)
		}
	}

	if err := clientA.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, readErr := clientA.Read(make([]byte, 1))
	timeout, ok := readErr.(net.Error)
	if !ok || !timeout.Timeout() || time.Since(started) > time.Second {
		t.Fatalf("read deadline error=%v elapsed=%s", readErr, time.Since(started))
	}
	if err := clientA.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = serverA.Write([]byte("a")) }()
	if payload := readMuxPayload(t, clientA, 1); string(payload) != "a" {
		t.Fatalf("deadline-clear payload=%q", payload)
	}

	// Timing out and closing A must not terminate B, and both streams must
	// still share the one authenticated underlying connection.
	_ = clientA.Close()
	_ = serverA.Close()
	go func() { _, _ = serverB.Write([]byte("still-alive")) }()
	if payload := readMuxPayload(t, clientB, len("still-alive")); string(payload) != "still-alive" {
		t.Fatalf("isolated stream payload=%q", payload)
	}
	if dials := dialer.dialCount(); dials != 1 {
		t.Fatalf("isolated streams used %d underlying WSS connections", dials)
	}

	_ = client.Close()
	_ = clientB.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientB.Read(make([]byte, 1)); err == nil {
		t.Fatal("logical stream survived mux client close")
	}
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("multiplex server did not stop after underlying client close")
	}
}

func dialMuxStream(t *testing.T, client interface {
	DialContext(context.Context, string, M.Socksaddr) (net.Conn, error)
}) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("usbip.mdd.internal", 3240))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func acceptMuxStream(t *testing.T, listener net.Listener) net.Conn {
	t.Helper()
	type result struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan result, 1)
	go func() {
		conn, err := listener.Accept()
		accepted <- result{conn: conn, err: err}
	}()
	select {
	case current := <-accepted:
		if current.err != nil {
			t.Fatal(current.err)
		}
		return current.conn
	case <-time.After(2 * time.Second):
		t.Fatal("multiplex server did not accept logical stream")
		return nil
	}
}

func readMuxPayload(t *testing.T, conn net.Conn, size int) []byte {
	t.Helper()
	payload := make([]byte, size)
	if _, err := io.ReadFull(conn, payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
