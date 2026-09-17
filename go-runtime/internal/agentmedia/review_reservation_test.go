package agentmedia

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

func TestReviewMediaRejectsReservationExpiringDuringUpgrade(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return mediaTestToken, nil }), now, 2)
	if err != nil {
		t.Fatal(err)
	}
	expires := now().Add(time.Minute)
	if err := broker.Reserve(Reservation{AgentID: "agent-1", ProcessGeneration: "process-1", SessionID: "session-1", MediaToken: mediaTestToken, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		broker.ServeHTTP(reviewExpiryWriter{w, func() { clock.Store(expires.UnixNano()) }}, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http://", "ws://", 1)+"/v1/agent/media/ws", &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer " + mediaTestToken}, "X-Mdd-Agent-Id": {"agent-1"}, "X-Mdd-Agent-Generation": {"process-1"}, "X-Mdd-Media-Session": {"session-1"}, "X-Mdd-Media-Token": {mediaTestToken},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	if _, payload, err := socket.Read(ctx); err == nil {
		t.Fatalf("expired bearer admitted: %s", payload)
	}
}
