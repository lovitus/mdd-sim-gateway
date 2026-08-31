package rawusb

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestConnListenerTransfersOnlyExplicitStreams(t *testing.T) {
	listener := newConnListener()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if err := listener.Enqueue(context.Background(), left); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-accepted:
		if got != left {
			t.Fatal("listener accepted a different stream")
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not accept the explicit stream")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Accept(); !errors.Is(err, errListenerClosed) {
		t.Fatalf("accept after close error=%v", err)
	}
}

func TestConnListenerClosesQueuedStream(t *testing.T) {
	listener := newConnListener()
	left, right := net.Pipe()
	defer right.Close()
	if err := listener.Enqueue(context.Background(), left); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	_ = right.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := right.Read(make([]byte, 1)); err == nil {
		t.Fatal("queued stream remained open after listener close")
	}
}

func TestConnListenerQueueIsBounded(t *testing.T) {
	listener := newConnListener()
	defer listener.Close()
	var peers []net.Conn
	defer func() {
		for _, peer := range peers {
			_ = peer.Close()
		}
	}()
	for index := 0; index < maximumPendingStreams; index++ {
		left, right := net.Pipe()
		peers = append(peers, right)
		if err := listener.Enqueue(context.Background(), left); err != nil {
			t.Fatalf("enqueue %d: %v", index, err)
		}
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if err := listener.Enqueue(context.Background(), left); err == nil {
		t.Fatal("listener accepted an unbounded stream queue")
	}
}
