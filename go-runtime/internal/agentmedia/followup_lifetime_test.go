package agentmedia

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

// Pause only after Acquire releases the broker lock and snapshots readiness.
// Ordinary goroutine scheduling can create exactly the same interleaving.
type followupAcquireContext struct {
	context.Context
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (ctx *followupAcquireContext) Done() <-chan struct{} {
	ctx.once.Do(func() {
		close(ctx.entered)
		if ctx.release != nil {
			<-ctx.release
		}
	})
	return ctx.Context.Done()
}

func TestFollowupMediaAcquireRejectsReplacement(t *testing.T) {
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) {
		return mediaTestToken, nil
	}), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	attach := func(generation, token string) *Peer {
		t.Helper()
		if err := broker.Reserve(Reservation{AgentID: "agent-1", ProcessGeneration: generation,
			SessionID: "reused-session", MediaToken: token, ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		socket, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http://", "ws://", 1)+"/v1/agent/media/ws", &websocket.DialOptions{HTTPHeader: http.Header{
			"Authorization": {"Bearer " + mediaTestToken}, "X-Mdd-Agent-Id": {"agent-1"},
			"X-Mdd-Agent-Generation": {generation}, "X-Mdd-Media-Session": {"reused-session"}, "X-Mdd-Media-Token": {token},
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { socket.CloseNow() })
		if _, _, err := socket.Read(ctx); err != nil {
			t.Fatal(err)
		}
		broker.mu.Lock()
		record := broker.reservations["reused-session"]
		broker.mu.Unlock()
		select {
		case <-record.ready:
		case <-ctx.Done():
			t.Fatal("media attachment did not finish")
		}
		return record.peer
	}
	attach("process-old", strings.Repeat("a", 32))
	defer broker.Revoke("reused-session")
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	paused := &followupAcquireContext{Context: ctx, entered: make(chan struct{}), release: release}
	done := make(chan error, 1)
	go func() { _, err := broker.Acquire(paused, "reused-session"); done <- err }()
	select {
	case <-paused.entered:
	case <-ctx.Done():
		t.Fatal("Acquire did not snapshot the original reservation")
	}
	broker.Revoke("reused-session")
	replacement := attach("process-new", strings.Repeat("b", 32))
	unblock()
	if err := <-done; !errors.Is(err, ErrMediaNotReady) {
		t.Fatalf("stale media acquisition claimed the replacement: %v", err)
	}
	peer, err := broker.Acquire(ctx, "reused-session")
	if err != nil || peer != replacement {
		t.Fatalf("replacement was consumed by the stale waiter: %v", err)
	}
}

func TestFollowupMediaRevocationWakesAcquire(t *testing.T) {
	for _, action := range []string{"revoke", "disconnect", "expiry"} {
		t.Run(action, func(t *testing.T) {
			now := time.Now()
			broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return mediaTestToken, nil }), func() time.Time { return now }, 2)
			if err != nil {
				t.Fatal(err)
			}
			if err := broker.Reserve(Reservation{AgentID: "agent-1", ProcessGeneration: "process-1", SessionID: "pending", MediaToken: mediaTestToken, ExpiresAt: now.Add(time.Minute)}); err != nil {
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
			case "disconnect":
				broker.DisconnectAgent("agent-1")
			case "expiry":
				// The production purge path is also called by Reserve and Acquire.
				broker.mu.Lock()
				broker.purgeLocked(now.Add(time.Minute))
				broker.mu.Unlock()
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrMediaNotReady) {
					t.Fatalf("revoked acquisition error=%v", err)
				}
			case <-time.After(time.Second):
				cancel()
				<-done
				t.Fatal("revoked media acquisition stayed blocked")
			}
		})
	}
}
