// SPDX-License-Identifier: AGPL-3.0-only

package outerudp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDialContextUsesSOCKS5UDPAssociation(t *testing.T) {
	proxyAddress, observed, tcpClosed, shutdown := startSOCKS5UDPServer(t)
	defer shutdown()
	connection, err := dialContext(
		context.Background(), "socks5://"+proxyAddress, "192.0.2.10:4500", time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("proxy-request")); err != nil {
		t.Fatal(err)
	}
	if got := receiveDatagram(t, observed); !bytes.Equal(got, []byte("proxy-request")) {
		t.Fatalf("SOCKS relay received=%q", got)
	}
	buffer := make([]byte, 64)
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := connection.Read(buffer)
	if err != nil || !bytes.Equal(buffer[:n], []byte("proxy-response")) {
		t.Fatalf("SOCKS relay response=%q err=%v", buffer[:n], err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tcpClosed:
	case <-time.After(time.Second):
		t.Fatal("closing UDP association did not close SOCKS control connection")
	}
}

func TestTransportSharesOneAssociationForIKEAndESP(t *testing.T) {
	connection := newDatagramConn()
	var dials atomic.Int32
	transport, err := New(Config{DialContext: func(context.Context, string, string, time.Duration) (net.Conn, error) {
		dials.Add(1)
		return connection, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("192.0.2.10:4500", time.Second); err != nil {
		t.Fatal(err)
	}

	response := make(chan []byte, 1)
	errResult := make(chan error, 1)
	go func() {
		value, exchangeErr := transport.ExchangeIKE(context.Background(), []byte("ike-request"))
		response <- value
		errResult <- exchangeErr
	}()
	wantIKEWire := append([]byte{0, 0, 0, 0}, []byte("ike-request")...)
	if got := receiveDatagram(t, connection.outbound); !bytes.Equal(got, wantIKEWire) {
		t.Fatalf("IKE wire packet=%x, want %x", got, wantIKEWire)
	}
	connection.inbound <- append([]byte{0, 0, 0, 0}, []byte("ike-response")...)
	if got := <-response; !bytes.Equal(got, []byte("ike-response")) {
		t.Fatalf("IKE response=%x", got)
	}
	if err := <-errResult; err != nil {
		t.Fatal(err)
	}

	espPacket := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	if err := transport.SendESPPacket(context.Background(), espPacket); err != nil {
		t.Fatal(err)
	}
	if got := receiveDatagram(t, connection.outbound); !bytes.Equal(got, espPacket) {
		t.Fatalf("ESP wire packet=%x, want %x", got, espPacket)
	}
	connection.inbound <- append([]byte(nil), espPacket...)
	gotESP, err := transport.ReadESPPacket(context.Background())
	if err != nil || !bytes.Equal(gotESP, espPacket) {
		t.Fatalf("ESP response=%x err=%v", gotESP, err)
	}

	if err := transport.SendNATTKeepalive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := receiveDatagram(t, connection.outbound); !bytes.Equal(got, []byte{0xff}) {
		t.Fatalf("NAT-T keepalive=%x", got)
	}
	if dials.Load() != 1 {
		t.Fatalf("dial count=%d, want 1", dials.Load())
	}
	if transport.LocalNetworkAddr() == nil {
		t.Fatal("local network address is nil after dial")
	}
	if err := transport.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if connection.closeCount.Load() != 1 {
		t.Fatalf("connection close count=%d, want 1", connection.closeCount.Load())
	}
}

func TestTransportQueuesEarlyESPWhileWaitingForMarkedIKE(t *testing.T) {
	connection := newDatagramConn()
	transport, err := New(Config{DialContext: func(context.Context, string, string, time.Duration) (net.Conn, error) {
		return connection, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("192.0.2.10:4500", time.Second); err != nil {
		t.Fatal(err)
	}

	firstResult := make(chan error, 1)
	go func() {
		_, exchangeErr := transport.ExchangeIKE(context.Background(), []byte("initial"))
		firstResult <- exchangeErr
	}()
	_ = receiveDatagram(t, connection.outbound)
	connection.inbound <- append([]byte{0, 0, 0, 0}, []byte("initial-response")...)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}

	response := make(chan []byte, 1)
	responseErr := make(chan error, 1)
	go func() {
		value, exchangeErr := transport.ExchangeIKE(context.Background(), []byte("final-auth"))
		response <- value
		responseErr <- exchangeErr
	}()
	_ = receiveDatagram(t, connection.outbound)
	earlyESP := bytes.Repeat([]byte{0x52}, 776)
	connection.inbound <- earlyESP
	connection.inbound <- append([]byte{0, 0, 0, 0}, []byte("final-response")...)
	if got := <-response; !bytes.Equal(got, []byte("final-response")) {
		t.Fatalf("IKE response=%x", got)
	}
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	gotESP, err := transport.ReadESPPacket(context.Background())
	if err != nil || !bytes.Equal(gotESP, earlyESP) {
		t.Fatalf("early ESP=%x err=%v", gotESP, err)
	}
}

func TestTransportTriesResolvedCandidatesOnlyBeforeFirstIKEResponse(t *testing.T) {
	connection := newDatagramConn()
	var resolved atomic.Int32
	var dialMu sync.Mutex
	var dialed []string
	transport, err := New(Config{
		ResolveContext: func(context.Context, string, string) ([]netip.Addr, error) {
			resolved.Add(1)
			return []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.11")}, nil
		},
		DialContext: func(_ context.Context, _, remote string, _ time.Duration) (net.Conn, error) {
			dialMu.Lock()
			dialed = append(dialed, remote)
			dialMu.Unlock()
			if remote == "192.0.2.10:4500" {
				return nil, errors.New("candidate unreachable")
			}
			return connection, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("epdg.example:4500", time.Second); err != nil {
		t.Fatal(err)
	}

	first := make(chan []byte, 1)
	firstErr := make(chan error, 1)
	go func() {
		response, exchangeErr := transport.ExchangeIKE(context.Background(), []byte("first"))
		first <- response
		firstErr <- exchangeErr
	}()
	if got := receiveDatagram(t, connection.outbound); !bytes.Equal(got, append([]byte{0, 0, 0, 0}, []byte("first")...)) {
		t.Fatalf("first IKE wire packet=%x", got)
	}
	connection.inbound <- append([]byte{0, 0, 0, 0}, []byte("first-response")...)
	if got := <-first; !bytes.Equal(got, []byte("first-response")) {
		t.Fatalf("first response=%x", got)
	}
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}

	second := make(chan []byte, 1)
	secondErr := make(chan error, 1)
	go func() {
		response, exchangeErr := transport.ExchangeIKE(context.Background(), []byte("second"))
		second <- response
		secondErr <- exchangeErr
	}()
	_ = receiveDatagram(t, connection.outbound)
	connection.inbound <- append([]byte{0, 0, 0, 0}, []byte("second-response")...)
	if got := <-second; !bytes.Equal(got, []byte("second-response")) {
		t.Fatalf("second response=%x", got)
	}
	if err := <-secondErr; err != nil {
		t.Fatal(err)
	}

	dialMu.Lock()
	gotDialed := append([]string(nil), dialed...)
	dialMu.Unlock()
	wantDialed := []string{"192.0.2.10:4500", "192.0.2.11:4500"}
	if len(gotDialed) != len(wantDialed) || gotDialed[0] != wantDialed[0] || gotDialed[1] != wantDialed[1] {
		t.Fatalf("dialed=%v, want %v", gotDialed, wantDialed)
	}
	if resolved.Load() != 1 {
		t.Fatalf("resolve count=%d, want 1", resolved.Load())
	}
	if selected := transport.SelectedRemote(); selected != "192.0.2.11:4500" {
		t.Fatalf("selected remote=%q, want second candidate", selected)
	}
}

func TestTransportDoesNotTryAnotherCandidateAfterAnyIKEResponse(t *testing.T) {
	connection := newDatagramConn()
	var dials atomic.Int32
	transport, err := New(Config{
		ResolveContext: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.11")}, nil
		},
		DialContext: func(context.Context, string, string, time.Duration) (net.Conn, error) {
			dials.Add(1)
			return connection, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("epdg.example:4500", time.Second); err != nil {
		t.Fatal(err)
	}
	result := make(chan []byte, 1)
	go func() {
		response, _ := transport.ExchangeIKE(context.Background(), []byte("request"))
		result <- response
	}()
	_ = receiveDatagram(t, connection.outbound)
	// The IKE parser may later reject this payload. Transport selection still
	// pins the responder and must not start another AKA-capable exchange.
	connection.inbound <- append([]byte{0, 0, 0, 0}, []byte("protocol-reject")...)
	if got := <-result; !bytes.Equal(got, []byte("protocol-reject")) {
		t.Fatalf("response=%x", got)
	}
	if dials.Load() != 1 {
		t.Fatalf("dial count=%d, want 1", dials.Load())
	}
}

func TestTransportFallsBackToProxyDNSWhenLocalResolutionFails(t *testing.T) {
	connection := newDatagramConn()
	var remote string
	transport, err := New(Config{
		ResolveContext: func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, errors.New("local DNS unavailable")
		},
		DialContext: func(_ context.Context, _, candidate string, _ time.Duration) (net.Conn, error) {
			remote = candidate
			return connection, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("epdg.example:4500", time.Second); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, exchangeErr := transport.ExchangeIKE(context.Background(), []byte("request"))
		result <- exchangeErr
	}()
	_ = receiveDatagram(t, connection.outbound)
	connection.inbound <- append([]byte{0, 0, 0, 0}, []byte("response")...)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if remote != "epdg.example:4500" {
		t.Fatalf("remote=%q, want hostname fallback", remote)
	}
}

func TestTransportReportsAllUnreachableCandidates(t *testing.T) {
	transport, err := New(Config{
		ResolveContext: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.11")}, nil
		},
		DialContext: func(context.Context, string, string, time.Duration) (net.Conn, error) {
			return nil, errors.New("unreachable")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("epdg.example:4500", time.Second); err != nil {
		t.Fatal(err)
	}
	_, err = transport.ExchangeIKE(context.Background(), []byte("request"))
	if !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("exchange error=%v, want ErrNoEndpoint", err)
	}
}

func TestTransportRotatesCandidatesAcrossSeparateAttempts(t *testing.T) {
	var dialed []string
	transport, err := New(Config{
		CandidateOffset: 1,
		ResolveContext: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.11")}, nil
		},
		DialContext: func(_ context.Context, _, remote string, _ time.Duration) (net.Conn, error) {
			dialed = append(dialed, remote)
			return nil, errors.New("unreachable")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("epdg.example:4500", time.Second); err != nil {
		t.Fatal(err)
	}
	_, _ = transport.ExchangeIKE(context.Background(), []byte("request"))
	want := []string{"192.0.2.11:4500", "192.0.2.10:4500"}
	if len(dialed) != len(want) || dialed[0] != want[0] || dialed[1] != want[1] {
		t.Fatalf("dialed=%v, want rotated order %v", dialed, want)
	}
}

func TestTransportCloseUnblocksRead(t *testing.T) {
	connection := newDatagramConn()
	transport, err := New(Config{DialContext: func(context.Context, string, string, time.Duration) (net.Conn, error) {
		return connection, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("192.0.2.10:4500", time.Second); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, readErr := transport.ReadESPPacket(context.Background())
		result <- readErr
	}()
	if err := transport.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("read error=%v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not unblock after close")
	}
}

func TestTransportQueueOverflowFailsClosed(t *testing.T) {
	connection := newDatagramConn()
	transport, err := New(Config{
		QueueCapacity: 1,
		DialContext: func(context.Context, string, string, time.Duration) (net.Conn, error) {
			return connection, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("192.0.2.10:4500", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := transport.SendNATTKeepalive(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = receiveDatagram(t, connection.outbound)
	connection.inbound <- append([]byte{0, 0, 0, 0}, []byte("first-ike")...)
	connection.inbound <- append([]byte{0, 0, 0, 0}, []byte("second-ike")...)
	select {
	case <-transport.done:
		if !errors.Is(transport.err(), ErrQueueOverflow) {
			t.Fatalf("transport error=%v, want queue overflow", transport.err())
		}
	case <-time.After(time.Second):
		t.Fatal("queue overflow did not fail the transport")
	}
}

func TestTransportDropsESPOverflowWithoutClosing(t *testing.T) {
	connection := newDatagramConn()
	transport, err := New(Config{
		QueueCapacity: 1,
		DialContext: func(context.Context, string, string, time.Duration) (net.Conn, error) {
			return connection, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("192.0.2.10:4500", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := transport.SendNATTKeepalive(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = receiveDatagram(t, connection.outbound)
	first := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	connection.inbound <- first
	connection.inbound <- []byte{9, 10, 11, 12, 13, 14, 15, 16}
	connection.inbound <- []byte{17, 18, 19, 20, 21, 22, 23, 24}
	deadline := time.Now().Add(time.Second)
	for connection.readCount.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if connection.readCount.Load() < 3 {
		t.Fatal("transport did not consume overflow packets")
	}
	got, err := transport.ReadESPPacket(context.Background())
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("queued ESP=%x err=%v", got, err)
	}
	select {
	case <-transport.done:
		t.Fatalf("ESP overflow closed transport: %v", transport.err())
	default:
	}
	if err := transport.SendNATTKeepalive(context.Background()); err != nil {
		t.Fatalf("transport unusable after ESP overflow: %v", err)
	}
}

func TestTransportRejectsInvalidConfigurationAndBinding(t *testing.T) {
	for _, proxy := range []string{
		"http://127.0.0.1:1080", "socks5://127.0.0.1", "socks5://user@127.0.0.1:1080",
		"socks5://127.0.0.1:0", "socks5://127.0.0.1:1080/path",
	} {
		if _, err := New(Config{ProxyURL: proxy}); err == nil {
			t.Fatalf("invalid proxy %q was accepted", proxy)
		}
	}
	transport, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("missing-port", time.Second); err == nil {
		t.Fatal("remote without port was accepted")
	}
	if err := transport.Bind("192.0.2.10:4500", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := transport.Bind("192.0.2.11:4500", time.Second); err == nil {
		t.Fatal("second remote endpoint was accepted")
	}
}

func receiveDatagram(t *testing.T, packets <-chan []byte) []byte {
	t.Helper()
	select {
	case packet := <-packets:
		return packet
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for datagram")
		return nil
	}
}

type datagramConn struct {
	inbound, outbound chan []byte
	closed            chan struct{}
	closeOnce         sync.Once
	closeCount        atomic.Int32
	readCount         atomic.Int32
}

func newDatagramConn() *datagramConn {
	return &datagramConn{
		inbound: make(chan []byte, 8), outbound: make(chan []byte, 8), closed: make(chan struct{}),
	}
}

func (connection *datagramConn) Read(buffer []byte) (int, error) {
	select {
	case packet := <-connection.inbound:
		connection.readCount.Add(1)
		return copy(buffer, packet), nil
	case <-connection.closed:
		return 0, net.ErrClosed
	}
}

func (connection *datagramConn) Write(packet []byte) (int, error) {
	copyPacket := append([]byte(nil), packet...)
	select {
	case connection.outbound <- copyPacket:
		return len(packet), nil
	case <-connection.closed:
		return 0, net.ErrClosed
	}
}

func (connection *datagramConn) Close() error {
	connection.closeOnce.Do(func() {
		connection.closeCount.Add(1)
		close(connection.closed)
	})
	return nil
}

func (connection *datagramConn) LocalAddr() net.Addr              { return staticAddr("local") }
func (connection *datagramConn) RemoteAddr() net.Addr             { return staticAddr("remote") }
func (connection *datagramConn) SetDeadline(time.Time) error      { return nil }
func (connection *datagramConn) SetReadDeadline(time.Time) error  { return nil }
func (connection *datagramConn) SetWriteDeadline(time.Time) error { return nil }

type staticAddr string

func (address staticAddr) Network() string { return "udp" }
func (address staticAddr) String() string  { return string(address) }

func startSOCKS5UDPServer(t *testing.T) (string, <-chan []byte, <-chan struct{}, func()) {
	t.Helper()
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		_ = tcpListener.Close()
		t.Fatal(err)
	}
	observed := make(chan []byte, 1)
	tcpClosed := make(chan struct{})
	serverErrors := make(chan error, 2)
	go func() {
		connection, acceptErr := tcpListener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(connection, greeting); err != nil {
			serverErrors <- err
			return
		}
		if !bytes.Equal(greeting, []byte{5, 1, 0}) {
			serverErrors <- errors.New("unexpected SOCKS greeting")
			return
		}
		if _, err := connection.Write([]byte{5, 0}); err != nil {
			serverErrors <- err
			return
		}
		request := make([]byte, 10)
		if _, err := io.ReadFull(connection, request); err != nil {
			serverErrors <- err
			return
		}
		if request[0] != 5 || request[1] != 3 || request[3] != 1 {
			serverErrors <- errors.New("unexpected SOCKS UDP ASSOCIATE request")
			return
		}
		relay := udpListener.LocalAddr().(*net.UDPAddr)
		reply := []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}
		binary.BigEndian.PutUint16(reply[8:], uint16(relay.Port))
		if _, err := connection.Write(reply); err != nil {
			serverErrors <- err
			return
		}
		var one [1]byte
		_, _ = connection.Read(one[:])
		close(tcpClosed)
	}()
	go func() {
		buffer := make([]byte, 2048)
		n, client, readErr := udpListener.ReadFromUDP(buffer)
		if readErr != nil {
			return
		}
		if n < 10 || buffer[0] != 0 || buffer[1] != 0 || buffer[2] != 0 || buffer[3] != 1 {
			serverErrors <- errors.New("unexpected SOCKS UDP datagram")
			return
		}
		observed <- append([]byte(nil), buffer[10:n]...)
		response := append([]byte(nil), buffer[:10]...)
		response = append(response, []byte("proxy-response")...)
		if _, err := udpListener.WriteToUDP(response, client); err != nil {
			serverErrors <- err
		}
	}()
	shutdown := func() {
		_ = tcpListener.Close()
		_ = udpListener.Close()
		select {
		case err := <-serverErrors:
			t.Errorf("SOCKS test server: %v", err)
		default:
		}
	}
	return tcpListener.Addr().String(), observed, tcpClosed, shutdown
}
