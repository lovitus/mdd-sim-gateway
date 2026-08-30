package cellularmedia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const serviceTestToken = "0123456789abcdef0123456789abcdef"

type fakeBrowserAuth struct{}

func (fakeBrowserAuth) VerifyBrowserSession(_ context.Context, request *http.Request) (string, error) {
	cookie, err := request.Cookie("test-session")
	if err != nil || cookie.Value != "browser-token" {
		return "", errors.New("not authenticated")
	}
	return "admin-1", nil
}

func (fakeBrowserAuth) AuthorizeBrowserMutation(request *http.Request) (string, error) {
	subject, err := (fakeBrowserAuth{}).VerifyBrowserSession(request.Context(), request)
	if err != nil || request.Header.Get("X-MDD-CSRF-Token") != "csrf-1" {
		return "", errors.New("mutation denied")
	}
	return subject, nil
}

type fakeCatalog struct{ line linecatalog.Line }

func (catalog fakeCatalog) Get(id string) (linecatalog.Line, error) {
	if id != catalog.line.ID {
		return linecatalog.Line{}, linecatalog.ErrNotFound
	}
	return catalog.line, nil
}

type fakeAgentRuntime struct {
	mu       sync.Mutex
	mediaURL string
	client   *http.Client
	socket   *websocket.Conn
	dials    int
	renewals int
	hangups  int
	hungUp   chan struct{}
}

func (runtime *fakeAgentRuntime) ResolveModemTarget(equipmentID, cardID string) (agentlink.ModemTarget, error) {
	if equipmentID != "862547055201716" || cardID != "8985200000000000001" {
		return agentlink.ModemTarget{}, agentlink.ErrModemOffline
	}
	return agentlink.ModemTarget{
		AgentID: "agent-1", ProcessGeneration: "generation-1", AttachmentID: "attachment-1",
		EquipmentID: equipmentID, CardID: cardID,
	}, nil
}

func (runtime *fakeAgentRuntime) ExecuteModemMedia(ctx context.Context, agentID, generation string, request agentlink.ModemMediaRequest) (agentlink.ModemMediaResponse, error) {
	response := agentlink.ModemMediaResponse{
		OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID, SessionID: request.SessionID,
	}
	if agentID != "agent-1" || generation != "generation-1" {
		return response, agentlink.ErrGenerationMismatch
	}
	if request.Action == agentlink.ModemMediaStop {
		runtime.mu.Lock()
		if runtime.socket != nil {
			runtime.socket.CloseNow()
			runtime.socket = nil
		}
		runtime.mu.Unlock()
		response.State = "stopped"
		return response, nil
	}
	headers := http.Header{
		"Authorization":          {"Bearer " + serviceTestToken},
		"X-MDD-Agent-ID":         {"agent-1"},
		"X-MDD-Agent-Generation": {"generation-1"},
		"X-MDD-Media-Session":    {request.SessionID},
		"X-MDD-Media-Token":      {request.MediaToken},
	}
	socket, _, err := websocket.Dial(ctx, runtime.mediaURL, &websocket.DialOptions{
		HTTPClient: runtime.client, HTTPHeader: headers, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return response, err
	}
	messageType, _, err := socket.Read(ctx)
	if err != nil || messageType != websocket.MessageText {
		socket.CloseNow()
		return response, errors.New("missing broker acknowledgement")
	}
	runtime.mu.Lock()
	runtime.socket = socket
	runtime.mu.Unlock()
	go func() {
		for {
			messageType, payload, err := socket.Read(context.Background())
			if err != nil {
				return
			}
			if messageType == websocket.MessageBinary {
				_ = socket.Write(context.Background(), websocket.MessageBinary, payload)
			}
		}
	}()
	response.State = "ready"
	return response, nil
}

func (runtime *fakeAgentRuntime) ExecuteModem(_ context.Context, agentID, generation string, request agentlink.ModemRequest) (agentlink.ModemResponse, error) {
	response := agentlink.ModemResponse{
		OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
	}
	if agentID != "agent-1" || generation != "generation-1" {
		return response, agentlink.ErrGenerationMismatch
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	switch request.Action {
	case agentlink.ModemCallDial:
		runtime.dials++
		response.Call = &agentlink.ModemCallResult{
			State: "dialing", Direction: "out", Number: request.Number,
			ObservedAt: time.Now(), Authoritative: true,
		}
		response.Lease = &agentlink.ModemLeaseResult{LeaseID: request.LeaseID, ExpiresAt: time.Now().Add(10 * time.Second)}
	case agentlink.ModemCallRenew:
		runtime.renewals++
		response.Lease = &agentlink.ModemLeaseResult{LeaseID: request.LeaseID, ExpiresAt: time.Now().Add(10 * time.Second)}
	case agentlink.ModemCallHangup:
		runtime.hangups++
		response.Call = &agentlink.ModemCallResult{
			State: "idle", ObservedAt: time.Now(), Authoritative: true,
			TerminalConfirmed: true, Strategy: "chup",
		}
		select {
		case <-runtime.hungUp:
		default:
			close(runtime.hungUp)
		}
	}
	return response, nil
}

func TestCellularMediaCanaryDialAndTenSecondGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker, err := agentmedia.NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) {
		return serviceTestToken, nil
	}), nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var clockMu sync.Mutex
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	runtime := &fakeAgentRuntime{hungUp: make(chan struct{})}
	service, err := New(Config{
		Context: ctx, Auth: fakeBrowserAuth{}, Agents: runtime, Broker: broker, Now: clock,
		Catalog: fakeCatalog{line: linecatalog.Line{
			SchemaVersion: 1, ID: "line-1", Enabled: true, CardID: "8985200000000000001",
			SIM: linecatalog.SIMConfig{IMEI: "862547055201716"},
		}},
	})
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

	leaseResponse := doJSON(t, server.Client(), http.MethodPost, server.URL+"/v1/cellular/media/leases",
		map[string]string{"line_id": "line-1", "call_id": "call-1", "expected_card_id": "8985200000000000001"})
	if leaseResponse.StatusCode != http.StatusCreated {
		t.Fatalf("lease status=%d body=%s", leaseResponse.StatusCode, readBody(leaseResponse))
	}
	var lease struct {
		SessionID string `json:"session_id"`
		WSPath    string `json:"ws_path"`
	}
	if json.NewDecoder(leaseResponse.Body).Decode(&lease) != nil || lease.SessionID == "" {
		t.Fatal("invalid cellular media lease response")
	}
	leaseResponse.Body.Close()
	browserURL := strings.Replace(server.URL, "http://", "ws://", 1) + lease.WSPath
	browser, _, err := websocket.Dial(context.Background(), browserURL, &websocket.DialOptions{HTTPHeader: http.Header{
		"Origin": {server.URL}, "Cookie": {"test-session=browser-token"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.CloseNow()
	writeBrowserJSON(t, browser, map[string]any{
		"type": "browser.media.hello", "version": 1, "session_id": lease.SessionID, "ticket": "call-1",
	})
	claimed := readBrowserJSON(t, browser)
	challenge := claimed["challenge"].(string)
	if claimed["type"] != "browser.media.claimed" || challenge == "" {
		t.Fatalf("claimed=%+v", claimed)
	}
	if started := readBrowserJSON(t, browser); started["type"] != "browser.media.started" || started["purpose"] != "canary" {
		t.Fatalf("started=%+v", started)
	}
	frame := make([]byte, pcmFrameBytes)
	for index := range frame {
		frame[index] = 0x20
	}
	for range 5 {
		if err := browser.Write(context.Background(), websocket.MessageBinary, frame); err != nil {
			t.Fatal(err)
		}
	}
	writeBrowserJSON(t, browser, map[string]any{
		"type": "browser.media.evidence", "version": 1, "challenge": challenge,
		"capture_callbacks": 2, "playback_callbacks": 2, "played_frames": 2,
	})
	ready := false
	readContext, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelRead()
	for !ready {
		messageType, payload, err := browser.Read(readContext)
		if err != nil {
			t.Fatal(err)
		}
		if messageType == websocket.MessageText {
			var message map[string]any
			if json.Unmarshal(payload, &message) == nil && message["type"] == "browser.media.ready" {
				ready = true
			}
		}
	}

	wrongCard := doJSON(t, server.Client(), http.MethodPost,
		server.URL+"/v1/lines/line-1/cellular/calls/start", map[string]string{
			"operation_id": "wrong-card", "session_id": lease.SessionID, "callee": "+85222333322",
			"expected_card_id": "8985200000000000999",
		})
	if wrongCard.StatusCode != http.StatusConflict || !strings.Contains(readBody(wrongCard), "paid_action_card_mismatch") {
		t.Fatal("wrong SIM identity was not rejected before cellular dial")
	}
	runtime.mu.Lock()
	unsafeDials := runtime.dials
	runtime.mu.Unlock()
	if unsafeDials != 0 {
		t.Fatalf("wrong SIM identity reached Agent dial: count=%d", unsafeDials)
	}

	startResponse := doJSON(t, server.Client(), http.MethodPost,
		server.URL+"/v1/lines/line-1/cellular/calls/start", map[string]string{
			"operation_id": "dial-1", "session_id": lease.SessionID, "callee": "+85222333322",
			"expected_card_id": "8985200000000000001",
		})
	if startResponse.StatusCode != http.StatusOK {
		t.Fatalf("start status=%d body=%s", startResponse.StatusCode, readBody(startResponse))
	}
	startResponse.Body.Close()
	runtime.mu.Lock()
	dials := runtime.dials
	runtime.mu.Unlock()
	if dials != 1 {
		t.Fatalf("dial count=%d", dials)
	}

	clockMu.Lock()
	now = now.Add(11 * time.Second)
	clockMu.Unlock()
	service.sweep(clock())
	select {
	case <-runtime.hungUp:
	case <-time.After(3 * time.Second):
		t.Fatal("10-second browser heartbeat guard did not hang up")
	}
	runtime.mu.Lock()
	hangups := runtime.hangups
	runtime.mu.Unlock()
	if hangups != 1 {
		t.Fatalf("hangup count=%d", hangups)
	}
	service.sweep(clock().Add(time.Minute))
	time.Sleep(50 * time.Millisecond)
	runtime.mu.Lock()
	hangups = runtime.hangups
	runtime.mu.Unlock()
	if hangups != 1 {
		t.Fatalf("terminal guard repeated hangup: count=%d", hangups)
	}
}

func TestCallStartUncertaintyClassification(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		uncertain bool
	}{
		{"lost Core Agent response", errors.New("connection closed"), true},
		{"Agent operation timeout", &agentlink.RemoteError{Kind: "transport", Code: "modem_operation_timeout"}, true},
		{"Agent retained paid lease", &agentlink.RemoteError{Kind: "failed", Code: "modem_call_start_uncertain"}, true},
		{"definite AT unavailable", &agentlink.RemoteError{Kind: "not_ready", Code: "modem_at_unavailable"}, false},
		{"definite lease conflict", &agentlink.RemoteError{Kind: "conflict", Code: "modem_call_lease_conflict"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := callStartUncertain(test.err); got != test.uncertain {
				t.Fatalf("callStartUncertain(%v)=%t want %t", test.err, got, test.uncertain)
			}
		})
	}
}

func TestCellularTelephoneContractMatchesAgentDialContract(t *testing.T) {
	for _, value := range []string{"+448001076285", "555", "1"} {
		if !validTelephone(value) {
			t.Errorf("valid cellular number rejected: %q", value)
		}
	}
	for _, value := range []string{"", "+", "12 34", "ATD123"} {
		if validTelephone(value) {
			t.Errorf("invalid cellular number accepted: %q", value)
		}
	}
}

func TestPrepareDoesNotTreatVoWiFiProviderEnabledAsCellularCapability(t *testing.T) {
	line := linecatalog.Line{
		SchemaVersion: 1, ID: "cellular-only", Enabled: false, CardID: "8985200000000000001",
		SIM: linecatalog.SIMConfig{IMEI: "862547055201716"},
	}
	equipmentID, cardID, ready := cellularTargetIdentity(line)
	if line.Enabled || !ready || equipmentID != line.SIM.IMEI || cardID != line.CardID {
		t.Fatalf("cellular-only target was rejected: equipment=%q card=%q ready=%t", equipmentID, cardID, ready)
	}
}

func doJSON(t *testing.T, client *http.Client, method, url string, value any) *http.Response {
	t.Helper()
	payload, _ := json.Marshal(value)
	request, _ := http.NewRequest(method, url, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", "test-session=browser-token")
	request.Header.Set("X-MDD-CSRF-Token", "csrf-1")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readBody(response *http.Response) string {
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	return string(payload)
}

func writeBrowserJSON(t *testing.T, socket *websocket.Conn, value any) {
	t.Helper()
	payload, _ := json.Marshal(value)
	if err := socket.Write(context.Background(), websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func readBrowserJSON(t *testing.T, socket *websocket.Conn) map[string]any {
	t.Helper()
	messageType, payload, err := socket.Read(context.Background())
	if err != nil || messageType != websocket.MessageText {
		t.Fatalf("browser JSON type=%v err=%v", messageType, err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
