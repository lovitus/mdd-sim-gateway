// SPDX-License-Identifier: AGPL-3.0-only

package browsermedia

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
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
		return mediaproxy.Target{URL: "ws" + strings.TrimPrefix(provider.URL, "http") + "/v1/media/session-1", Token: testToken}, nil
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
	session, found := registry.Session("session-1")
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
	socket, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/media/session-1", options)
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
	if session, found := registry.Session("session-1"); !found || session.Ready() {
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

func TestReadySessionCarriesLivePCMAndResumes(t *testing.T) {
	registry, err := NewRegistry(testToken, 1)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry)
	defer server.Close()
	options := &websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer " + testToken}}}
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/media/session-live"
	socket, _, err := websocket.Dial(context.Background(), url, options)
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, socket, map[string]any{
		"type": "browser.media.hello", "version": 1, "session_id": "session-live", "ticket": "ticket-1",
	})
	claimed := readText(t, socket)
	challenge, _ := claimed["challenge"].(string)
	resumeTicket, _ := claimed["resume_ticket"].(string)
	_ = readText(t, socket)
	for index := 0; index < 2; index++ {
		frame := signalFrame(int16(1000 + index))
		if err := socket.Write(context.Background(), websocket.MessageBinary, frame); err != nil {
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
	_ = readText(t, socket)
	_ = readText(t, socket)
	session, found := registry.Session("session-live")
	if !found {
		t.Fatal("session not found")
	}
	stream := newFakeStream()
	if err := session.AttachStream(stream); err != nil {
		t.Fatal(err)
	}
	uplink := signalFrame(2300)
	if err := socket.Write(context.Background(), websocket.MessageBinary, uplink); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stream.input:
		if string(got) != string(uplink) {
			t.Fatal("live uplink changed")
		}
	case <-time.After(time.Second):
		t.Fatal("live uplink was not delivered")
	}
	downlink := signalFrame(-1900)
	stream.output <- media.PCMFrame{Data: downlink, CapturedAt: time.Now()}
	if kind, got, err := socket.Read(context.Background()); err != nil || kind != websocket.MessageBinary || string(got) != string(downlink) {
		t.Fatalf("live downlink kind=%v len=%d err=%v", kind, len(got), err)
	}
	_ = socket.Close(websocket.StatusNormalClosure, "network changed")
	waitFor(t, time.Second, func() bool { return !session.Connected() })

	resumed, _, err := websocket.Dial(context.Background(), url, options)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.CloseNow()
	writeText(t, resumed, map[string]any{
		"type": "browser.media.resume", "version": 1, "session_id": "session-live",
		"resume_ticket": resumeTicket, "connection_epoch": 1,
	})
	if message := readText(t, resumed); message["type"] != "browser.media.resumed" {
		t.Fatalf("resume=%v", message)
	}
	if started := readText(t, resumed); started["type"] != "browser.media.started" || started["purpose"] != "call" {
		t.Fatalf("started=%v", started)
	}
	stream.output <- media.PCMFrame{Data: downlink, CapturedAt: time.Now()}
	if kind, _, err := resumed.Read(context.Background()); err != nil || kind != websocket.MessageBinary {
		t.Fatalf("resumed downlink kind=%v err=%v", kind, err)
	}
	session.EndStream("call ended")
	waitFor(t, time.Second, func() bool {
		_, found := registry.Session("session-live")
		return !found
	})
}

type fakeStream struct {
	input  chan []byte
	output chan media.PCMFrame
	errors chan error
}

func newFakeStream() *fakeStream {
	return &fakeStream{input: make(chan []byte, 4), output: make(chan media.PCMFrame, 4), errors: make(chan error)}
}

func (stream *fakeStream) WritePCM(frame []byte, _ time.Time) (bool, error) {
	if len(frame) != PCMFrameBytes {
		return false, errors.New("bad PCM")
	}
	stream.input <- append([]byte(nil), frame...)
	return true, nil
}

func (stream *fakeStream) PCM() <-chan media.PCMFrame { return stream.output }
func (stream *fakeStream) Errors() <-chan error       { return stream.errors }

func signalFrame(value int16) []byte {
	frame := make([]byte, PCMFrameBytes)
	for index := 0; index < 16; index++ {
		binary.LittleEndian.PutUint16(frame[index*2:], uint16(value))
	}
	return frame
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
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
