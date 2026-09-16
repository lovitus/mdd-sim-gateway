package agentdata

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

type reviewExpiryWriter struct {
	http.ResponseWriter
	expire func()
}

func (w reviewExpiryWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err == nil {
		w.expire()
	}
	return conn, rw, err
}

func TestReviewDataRejectsReservationExpiringDuringUpgrade(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return managerTestToken, nil }), now)
	if err != nil {
		t.Fatal(err)
	}
	expires := now().Add(time.Minute)
	if err := broker.Reserve(Reservation{AgentID: "agent-1", ProcessGeneration: "process-1", SessionID: "session-1", StreamID: "stream-1", StreamToken: managerTestToken, Network: "tcp", ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		broker.ServeHTTP(reviewExpiryWriter{w, func() { clock.Store(expires.UnixNano()) }}, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http://", "ws://", 1)+"/v1/agent/data/ws", &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer " + managerTestToken}, "X-Mdd-Agent-Id": {"agent-1"}, "X-Mdd-Agent-Generation": {"process-1"}, "X-Mdd-Data-Stream": {"stream-1"}, "X-Mdd-Data-Token": {managerTestToken},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	if _, payload, err := socket.Read(ctx); err == nil {
		t.Fatalf("expired bearer admitted: %s", payload)
	}
}

func TestReviewAcquireRechecksDataDeadlineAfterWait(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	entered := make(chan struct{})
	var capture atomic.Bool
	now := func() time.Time {
		current := time.Unix(0, clock.Load())
		if capture.CompareAndSwap(true, false) {
			close(entered)
		}
		return current
	}
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return managerTestToken, nil }), now)
	if err != nil {
		t.Fatal(err)
	}
	expires := now().Add(time.Minute)
	if err := broker.Reserve(Reservation{AgentID: "agent-1", ProcessGeneration: "process-1", SessionID: "session-1", StreamID: "stream-1", StreamToken: managerTestToken, Network: "tcp", ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	capture.Store(true)
	result := make(chan error, 1)
	go func() {
		conn, err := broker.Acquire(ctx, "stream-1")
		if conn != nil {
			_ = conn.Close()
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("Acquire did not check initial reservation")
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	broker.mu.Lock()
	clock.Store(expires.UnixNano())
	record := broker.items["stream-1"]
	record.conn = newTrackedConn(left)
	close(record.ready)
	broker.mu.Unlock()
	if err := <-result; err == nil {
		t.Fatal("Acquire returned a connection whose lease expired while waiting")
	}
}
