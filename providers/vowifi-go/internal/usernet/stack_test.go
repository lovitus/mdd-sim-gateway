// SPDX-License-Identifier: AGPL-3.0-only

package usernet

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type memorySession struct {
	inbound chan []byte
	peer    *memorySession
	closed  chan struct{}
	once    sync.Once
}

func memorySessionPair() (*memorySession, *memorySession) {
	first := &memorySession{inbound: make(chan []byte, 128), closed: make(chan struct{})}
	second := &memorySession{inbound: make(chan []byte, 128), closed: make(chan struct{})}
	first.peer = second
	second.peer = first
	return first, second
}

func (session *memorySession) Send(ctx context.Context, packet []byte) error {
	copyOfPacket := append([]byte(nil), packet...)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.peer.closed:
		return ErrClosed
	case session.peer.inbound <- copyOfPacket:
		return nil
	}
}

func (session *memorySession) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-session.closed:
		return nil, ErrClosed
	case packet := <-session.inbound:
		return append([]byte(nil), packet...), nil
	}
}

func (session *memorySession) Close(context.Context) error {
	session.once.Do(func() { close(session.closed) })
	return nil
}

func openStackPair(t *testing.T) (*Stack, *Stack) {
	t.Helper()
	firstPackets, secondPackets := memorySessionPair()
	first, err := Open(context.Background(), firstPackets, Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")},
		DNS:       []netip.Addr{netip.MustParseAddr("10.0.0.2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), secondPackets, Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.2")},
	})
	if err != nil {
		_ = first.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = first.Close(ctx)
		_ = second.Close(ctx)
	})
	return first, second
}

func TestInMemoryStackCarriesTCP(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	if clientStack.device.File() != nil || serverStack.device.File() != nil {
		t.Fatal("in-memory stack exposed an OS TUN file")
	}
	listener, err := serverStack.Listen(context.Background(), "tcp4", "10.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer connection.Close()
		request := make([]byte, 4)
		if _, err := io.ReadFull(connection, request); err != nil {
			serverResult <- err
			return
		}
		if string(request) != "ping" {
			serverResult <- errors.New("unexpected TCP request")
			return
		}
		_, err = connection.Write([]byte("pong"))
		serverResult <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := clientStack.DialContext(ctx, "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "pong" {
		t.Fatalf("TCP response = %q", response)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestInMemoryStackCarriesUDP(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	server, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	serverResult := make(chan error, 1)
	go func() {
		buffer := make([]byte, 32)
		count, source, err := server.ReadFrom(buffer)
		if err == nil && string(buffer[:count]) != "request" {
			err = errors.New("unexpected UDP request")
		}
		if err == nil {
			_, err = server.WriteTo([]byte("response"), source)
		}
		serverResult <- err
	}()
	client, err := clientStack.DialContext(context.Background(), "udp4", server.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 32)
	count, err := client.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(response[:count]) != "response" {
		t.Fatalf("UDP response = %q", response[:count])
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestInMemoryStackResolvesDNSOverSWuPackets(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	server, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:53")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	serverResult := make(chan error, 1)
	go func() {
		buffer := make([]byte, 512)
		count, source, err := server.ReadFrom(buffer)
		if err != nil {
			serverResult <- err
			return
		}
		var parser dnsmessage.Parser
		header, err := parser.Start(buffer[:count])
		if err != nil {
			serverResult <- err
			return
		}
		question, err := parser.Question()
		if err != nil {
			serverResult <- err
			return
		}
		builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
			ID: header.ID, Response: true, RecursionAvailable: true,
		})
		builder.EnableCompression()
		if err = builder.StartQuestions(); err == nil {
			err = builder.Question(question)
		}
		if err == nil {
			err = builder.StartAnswers()
		}
		if err == nil {
			err = builder.AResource(dnsmessage.ResourceHeader{
				Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30,
			}, dnsmessage.AResource{A: [4]byte{10, 0, 0, 9}})
		}
		var response []byte
		if err == nil {
			response, err = builder.Finish()
		}
		if err == nil {
			_, err = server.WriteTo(response, source)
		}
		serverResult <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addresses, err := clientStack.LookupNetIP(ctx, "ip4", "ims.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0] != netip.MustParseAddr("10.0.0.9") {
		t.Fatalf("DNS addresses = %v", addresses)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

type failingSession struct {
	failure error
	closed  chan struct{}
	once    sync.Once
}

func (session *failingSession) Send(ctx context.Context, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}
func (session *failingSession) Receive(context.Context) ([]byte, error) {
	return nil, session.failure
}
func (session *failingSession) Close(context.Context) error {
	session.once.Do(func() { close(session.closed) })
	return nil
}

func TestPacketPumpFailureIsObservableAndStopsOnlyStack(t *testing.T) {
	want := errors.New("SWu read failed")
	packets := &failingSession{failure: want, closed: make(chan struct{})}
	stack, err := Open(context.Background(), packets, Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stack.Errors():
		var failure *PumpError
		if !errors.As(got, &failure) || failure.Direction != PumpFromSWu || !errors.Is(got, want) {
			t.Fatalf("pump error = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("packet pump failure was not surfaced")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stack.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-packets.closed:
	default:
		t.Fatal("failed stack did not close its packet session")
	}
}
