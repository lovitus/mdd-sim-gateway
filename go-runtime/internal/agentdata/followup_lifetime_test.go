package agentdata

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

type followupAcquireContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (ctx *followupAcquireContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.entered) })
	return ctx.Context.Done()
}

func TestFollowupDataRevocationWakesAcquire(t *testing.T) {
	for _, action := range []string{"revoke", "session", "disconnect", "expiry"} {
		t.Run(action, func(t *testing.T) {
			now := time.Now()
			broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return managerTestToken, nil }), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if err := broker.Reserve(Reservation{AgentID: "agent-1", ProcessGeneration: "process-1", SessionID: "session-1", StreamID: "pending", StreamToken: managerTestToken, Network: "tcp", ExpiresAt: now.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			observed := &followupAcquireContext{Context: ctx, entered: make(chan struct{})}
			done := make(chan error, 1)
			go func() { _, err := broker.Acquire(observed, "pending"); done <- err }()
			select {
			case <-observed.entered:
			case <-ctx.Done():
				t.Fatal("Acquire did not enter its wait")
			}
			switch action {
			case "revoke":
				broker.Revoke("pending")
			case "session":
				broker.RevokeSession("session-1")
			case "disconnect":
				broker.DisconnectAgent("agent-1")
			case "expiry":
				broker.mu.Lock()
				broker.purgeLocked(now.Add(time.Minute))
				broker.mu.Unlock()
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrDataNotReady) {
					t.Fatalf("revoked acquisition error=%v", err)
				}
			case <-time.After(time.Second):
				cancel()
				<-done
				t.Fatal("revoked data acquisition stayed blocked")
			}
		})
	}
}

// The HTTP upgrade uses its original buffered writer; only the subsequent
// WebSocket acknowledgement hits the fault-injected connection writer.
type followupAckWriter struct {
	http.ResponseWriter
	fail func() error
}

func (writer followupAckWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := writer.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	// Flush the HTTP response before switching the buffered WebSocket writer.
	if err := rw.Writer.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	injected := &followupAckConn{Conn: conn, fail: writer.fail}
	rw.Writer.Reset(injected)
	return injected, rw, nil
}

type followupAckConn struct {
	net.Conn
	fail func() error
}

func (conn *followupAckConn) Write(payload []byte) (int, error) {
	return 0, conn.fail()
}

func TestFollowupDataAckFailurePreservesReplacement(t *testing.T) {
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return managerTestToken, nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	input := Reservation{AgentID: "agent-1", ProcessGeneration: "process-old", SessionID: "session-1", StreamID: "reused-stream", StreamToken: managerTestToken, Network: "tcp", ExpiresAt: time.Now().Add(time.Minute)}
	if err := broker.Reserve(input); err != nil {
		t.Fatal(err)
	}
	var old *reservation
	var injectionErr error
	var once sync.Once
	served := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(served)
		broker.ServeHTTP(followupAckWriter{w, func() error {
			once.Do(func() {
				// Reproduce the interval after Revoke deletes the old record,
				// before its connection close makes the acknowledgement fail.
				broker.mu.Lock()
				old = broker.items[input.StreamID]
				delete(broker.items, input.StreamID)
				broker.mu.Unlock()
				if old == nil || old.conn == nil {
					injectionErr = errors.New("fault did not occur at the acknowledgement boundary")
					return
				}
				replacement := input
				replacement.ProcessGeneration = "process-new"
				replacement.StreamToken = strings.Repeat("b", 32)
				injectionErr = broker.Reserve(replacement)
			})
			return errors.New("injected acknowledgement write failure")
		}}, r)
	}))
	defer server.Close()
	defer broker.Revoke(input.StreamID)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http://", "ws://", 1)+"/v1/agent/data/ws", &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer " + managerTestToken}, "X-Mdd-Agent-Id": {input.AgentID}, "X-Mdd-Agent-Generation": {input.ProcessGeneration}, "X-Mdd-Data-Stream": {input.StreamID}, "X-Mdd-Data-Token": {input.StreamToken},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	select {
	case <-served:
	case <-ctx.Done():
		t.Fatal("failed acknowledgement handler did not finish")
	}
	if old != nil && old.conn != nil {
		defer old.conn.Close()
	}
	if injectionErr != nil {
		t.Fatal(injectionErr)
	}
	broker.mu.Lock()
	replacement := broker.items[input.StreamID]
	broker.mu.Unlock()
	if replacement == nil || replacement.ProcessGeneration != "process-new" {
		t.Fatal("old acknowledgement failure revoked the replacement reservation")
	}
}
