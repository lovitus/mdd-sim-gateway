package cellularmedia

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func TestAnsweredIncomingCallDTMFKeepsExactSessionAndHeartbeatGuards(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker, err := agentmedia.NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return serviceTestToken, nil }), nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := callhistory.Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer calls.Close()
	now := time.Now().UTC()
	source := callhistory.CellularCallSource{IncomingEventID: "incoming-dtmf", LastEventID: "incoming-dtmf-event", Revision: 1,
		LineID: "line-1", AgentID: "agent-1", ProcessGeneration: "generation-1", AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "session-1", Occurrence: 3, NativeCallIndex: 6, State: "ringing_in", Direction: "in", Number: "+15550100129", FirstObservedAt: now, ObservedAt: now, ReceivedAt: now}
	if _, err := calls.AcceptCellularEvent(source); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeAgentRuntime{hungUp: make(chan struct{}), receiptCapability: true}
	service, err := New(Config{Context: ctx, Auth: fakeBrowserAuth{}, Agents: runtime, Broker: broker, Incoming: calls, Calls: calls, Recovery: calls, Now: func() time.Time { return now },
		Catalog: fakeCatalog{line: linecatalog.Line{SchemaVersion: 1, ID: source.LineID, CardID: source.CardID}}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	mux := http.NewServeMux()
	mux.Handle("/v1/agent/media/ws", broker)
	mux.Handle("/v1/cellular/media/leases", service)
	mux.Handle("GET /api/cellular-browser-media/{sessionID}/ws", service)
	mux.Handle("POST /v1/lines/{lineID}/cellular/calls/{operation}", service)
	server := httptest.NewServer(mux)
	defer server.Close()
	runtime.mediaURL = strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/agent/media/ws"
	runtime.client = server.Client()
	body := map[string]any{"line_id": source.LineID, "call_id": source.IncomingEventID, "operation_id": "answer-dtmf", "incoming_event_id": source.IncomingEventID,
		"expected_card_id": source.CardID, "sim_session_generation": source.SIMSessionGeneration, "native_call_index": source.NativeCallIndex, "call_occurrence": source.Occurrence, "recovery_key": strings.Repeat("r", 64)}
	response := doJSON(t, server.Client(), http.MethodPost, server.URL+"/v1/cellular/media/leases", body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("incoming lease: %d %s", response.StatusCode, readBody(response))
	}
	var lease struct {
		SessionID string `json:"session_id"`
		WSPath    string `json:"ws_path"`
	}
	if err := json.NewDecoder(response.Body).Decode(&lease); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	browser, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http://", "ws://", 1)+lease.WSPath, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {"test-session=browser-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.CloseNow()
	writeBrowserJSON(t, browser, map[string]any{"type": "browser.media.hello", "version": 1, "session_id": lease.SessionID, "ticket": source.IncomingEventID})
	claimed := readBrowserJSON(t, browser)
	if claimed["type"] != "browser.media.claimed" {
		t.Fatalf("claim=%v", claimed)
	}
	if message := readBrowserJSON(t, browser); message["purpose"] != "canary" {
		t.Fatalf("start=%v", message)
	}
	for range 5 {
		if err := browser.Write(ctx, websocket.MessageBinary, bytes.Repeat([]byte{0x20}, pcmFrameBytes)); err != nil {
			t.Fatal(err)
		}
	}
	writeBrowserJSON(t, browser, map[string]any{"type": "browser.media.evidence", "version": 1, "challenge": claimed["challenge"], "capture_callbacks": 2, "playback_callbacks": 2, "played_frames": 2})
	deadline, cancelRead := context.WithTimeout(ctx, 3*time.Second)
	defer cancelRead()
	for {
		kind, payload, err := browser.Read(deadline)
		if err != nil {
			t.Fatal(err)
		}
		var message map[string]any
		if kind == websocket.MessageText && json.Unmarshal(payload, &message) == nil && message["type"] == "browser.media.ready" {
			break
		}
	}
	delete(body, "line_id")
	delete(body, "call_id")
	delete(body, "recovery_key")
	body["session_id"] = lease.SessionID
	response = doJSON(t, server.Client(), http.MethodPost, server.URL+"/v1/lines/line-1/cellular/calls/answer", body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("answer: %d %s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
	current := service.lookup(lease.SessionID)
	if current == nil || current.direction != "in" {
		t.Fatal("answer did not preserve incoming session")
	}
	for _, tc := range []struct {
		name, subject, line, phase string
		stale                      bool
		code                       int
	}{
		{"other-owner", "another-admin", source.LineID, "active", false, http.StatusNotFound},
		{"other-line", "admin-1", "other-line", "active", false, http.StatusNotFound},
		{"not-active", "admin-1", source.LineID, "uncertain", false, http.StatusConflict},
		{"stale-heartbeat", "admin-1", source.LineID, "active", true, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := &session{id: current.id, subject: current.subject, lineID: current.lineID, direction: current.direction, target: current.target, phase: tc.phase, lastHeartbeat: now}
			if tc.stale {
				candidate.lastHeartbeat = now.Add(-heartbeatTimeout)
			}
			guarded := &Service{config: service.config, sessions: map[string]*session{candidate.id: candidate}}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"operation_id":"blocked-dtmf","session_id":"`+lease.SessionID+`","signal":"5"}`))
			result := httptest.NewRecorder()
			guarded.sendDTMF(result, request, tc.line, tc.subject)
			if result.Code != tc.code {
				t.Fatalf("code=%d body=%s", result.Code, result.Body.String())
			}
		})
	}
	response = doJSON(t, server.Client(), http.MethodPost, server.URL+"/v1/lines/line-1/cellular/calls/dtmf", map[string]string{"operation_id": "incoming-tone", "session_id": lease.SessionID, "signal": "5"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("incoming DTMF: %d %s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
	runtime.mu.Lock()
	answers, dials, tones, toneLease := runtime.answers, runtime.dials, runtime.dtmfs, runtime.dtmfLease
	runtime.mu.Unlock()
	if answers != 1 || dials != 0 || tones != 1 || toneLease != lease.SessionID {
		t.Fatalf("answers=%d dials=%d tones=%d exactLease=%t", answers, dials, tones, toneLease == lease.SessionID)
	}
}
