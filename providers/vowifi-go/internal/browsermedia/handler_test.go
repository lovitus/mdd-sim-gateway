// SPDX-License-Identifier: AGPL-3.0-only

package browsermedia

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestBrowserCanaryThroughCoreProxy(t *testing.T) {
	registry, err := NewRegistry(testToken, 2)
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(registry)
	defer provider.Close()
	proxy, err := mediaproxy.NewHandler(mediaproxy.AuthorizerFunc(func(context.Context, *http.Request) (mediaproxy.Target, error) {
		return mediaproxy.Target{URL: "ws" + strings.TrimPrefix(provider.URL, "http") + "/v1/media/call-1", Token: testToken}, nil
	}), nil, time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	core := httptest.NewServer(proxy)
	defer core.Close()
	socket, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(core.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	writeText(t, socket, map[string]any{
		"type": "browser.media.hello", "version": 1, "session_id": "session-1", "ticket": "ticket-1",
	})
	claimed := readText(t, socket)
	challenge, _ := claimed["challenge"].(string)
	if claimed["type"] != "browser.media.claimed" || challenge == "" {
		t.Fatalf("claimed=%v", claimed)
	}
	if started := readText(t, socket); started["type"] != "browser.media.started" {
		t.Fatalf("started=%v", started)
	}
	for index := 0; index < 2; index++ {
		frame := make([]byte, PCMFrameBytes)
		for sample := 0; sample < 16; sample++ {
			binary.LittleEndian.PutUint16(frame[sample*2:], uint16(1000+index))
		}
		if err := socket.Write(context.Background(), websocket.MessageBinary, frame); err != nil {
			t.Fatal(err)
		}
		kind, echoed, err := socket.Read(context.Background())
		if err != nil || kind != websocket.MessageBinary || len(echoed) != PCMFrameBytes || echoed[0] != frame[0] {
			t.Fatalf("kind=%v len=%d err=%v", kind, len(echoed), err)
		}
	}
	writeText(t, socket, map[string]any{
		"type": "browser.media.evidence", "version": 1, "challenge": challenge,
		"capture_callbacks": 2, "playback_callbacks": 2, "played_frames": 2,
	})
	if status := readText(t, socket); status["type"] != "browser.media.status" || status["ready"] != true {
		t.Fatalf("status=%v", status)
	}
	if ready := readText(t, socket); ready["type"] != "browser.media.ready" {
		t.Fatalf("ready=%v", ready)
	}
	session, found := registry.Session("call-1")
	if !found || !session.Ready() {
		t.Fatal("provider session is not ready")
	}
}

func TestBrowserCanaryRejectsSilentFrames(t *testing.T) {
	registry, err := NewRegistry(testToken, 1)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry)
	defer server.Close()
	options := &websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer " + testToken}}}
	socket, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/media/call-1", options)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	writeText(t, socket, map[string]any{"type": "browser.media.hello", "version": 1, "session_id": "session-1", "ticket": "ticket-1"})
	challenge, _ := readText(t, socket)["challenge"].(string)
	_ = readText(t, socket)
	for index := 0; index < 2; index++ {
		if err := socket.Write(context.Background(), websocket.MessageBinary, make([]byte, PCMFrameBytes)); err != nil {
			t.Fatal(err)
		}
		if kind, _, err := socket.Read(context.Background()); err != nil || kind != websocket.MessageBinary {
			t.Fatalf("kind=%v err=%v", kind, err)
		}
	}
	writeText(t, socket, map[string]any{
		"type": "browser.media.evidence", "version": 1, "challenge": challenge,
		"capture_callbacks": 2, "playback_callbacks": 2, "played_frames": 2,
	})
	if session, found := registry.Session("call-1"); !found || session.Ready() {
		t.Fatal("silent frames incorrectly made the canary ready")
	}
}

func TestBrowserProviderRejectsDuplicateSession(t *testing.T) {
	registry, err := NewRegistry(testToken, 1)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry)
	defer server.Close()
	options := &websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer " + testToken}}}
	first, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/media/call-1", options)
	if err != nil {
		t.Fatal(err)
	}
	defer first.CloseNow()
	_, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/media/call-1", options)
	if err == nil || response == nil || response.StatusCode != 409 {
		t.Fatalf("response=%v err=%v", response, err)
	}
}

func writeText(t *testing.T, socket *websocket.Conn, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.Write(context.Background(), websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, socket *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	kind, payload, err := socket.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("kind=%v err=%v", kind, err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
