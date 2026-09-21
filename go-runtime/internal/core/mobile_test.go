package core

import (
	"context"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMobileStreamRequiresAuthAndRevokesSession(t *testing.T) {
	verifier := &toggleBrowserVerifier{}
	server := NewServer(testReplay(t, time.Now()), time.Now, WithBrowserControl(verifier))
	server.browserEvery = 10 * time.Millisecond
	host := httptest.NewServer(server)
	defer host.Close()
	url := "ws" + strings.TrimPrefix(host.URL, "http") + "/v1/mobile/ws"
	if conn, response, err := websocket.Dial(t.Context(), url, nil); err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		if conn != nil {
			conn.CloseNow()
		}
		t.Fatalf("unauthorized=%v %v", response, err)
	}
	verifier.allowed.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var first mobileEnvelope
	if err = wsjson.Read(ctx, conn, &first); err != nil {
		t.Fatal(err)
	}
	if first.Type != "mobile.snapshot" || first.Sequence != 1 || first.Data == nil || first.SchemaVersion != 1 {
		t.Fatalf("bad first snapshot: %+v", first)
	}
	verifier.allowed.Store(false)
	var next mobileEnvelope
	if err = wsjson.Read(ctx, conn, &next); websocket.CloseStatus(err) != browserAuthClose {
		t.Fatalf("revocation did not close: %v", err)
	}
}
func TestMobileStreamRejectsCrossOrigin(t *testing.T) {
	verifier := &toggleBrowserVerifier{}
	verifier.allowed.Store(true)
	host := httptest.NewServer(NewServer(testReplay(t, time.Now()), time.Now, WithBrowserControl(verifier)))
	defer host.Close()
	conn, response, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(host.URL, "http")+"/v1/mobile/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://other.invalid"}}})
	if conn != nil {
		conn.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross origin=%v %v", response, err)
	}
}
