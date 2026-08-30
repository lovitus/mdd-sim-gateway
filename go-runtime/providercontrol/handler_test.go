package providercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeBackend struct {
	mu         sync.Mutex
	snapshot   vowifiipc.Snapshot
	operations []string
	messageErr error
}

type fakeIntentWriter struct {
	mu          sync.Mutex
	lineEnabled bool
	writes      []bool
	err         error
}

type fakeCatalog struct {
	line linecatalog.Line
	err  error
}

func (catalog fakeCatalog) Get(id string) (linecatalog.Line, error) {
	if catalog.err != nil {
		return linecatalog.Line{}, catalog.err
	}
	if id != catalog.line.ID {
		return linecatalog.Line{}, linecatalog.ErrNotFound
	}
	return catalog.line, nil
}

func paidActionCatalog() fakeCatalog {
	return fakeCatalog{line: linecatalog.Line{ID: "line-1", CardID: "8944100000000000001"}}
}

func (writer *fakeIntentWriter) SetRuntimeIntent(_ string, enabled bool) (bool, bool, uint64, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.writes = append(writer.writes, enabled)
	return writer.lineEnabled, true, uint64(len(writer.writes) + 1), writer.err
}

func newFakeBackend(generation string) *fakeBackend {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	return &fakeBackend{snapshot: vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-1", ProviderID: "provider-1",
		ProcessGeneration: generation, Sequence: 1, ObservedAt: time.Now().UTC(),
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning, Code: "ready"},
		Tunnel:  ready, IMS: ready, Voice: ready, Messaging: ready,
	}}
}

func (backend *fakeBackend) Status(context.Context) (vowifiipc.Snapshot, error) {
	return backend.snapshot, nil
}

func (backend *fakeBackend) Start(_ context.Context, input vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	backend.record("runtime/start")
	return backend.operation(input.OperationID, "started"), nil
}

func (backend *fakeBackend) Stop(_ context.Context, input vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	backend.record("runtime/stop")
	return backend.operation(input.OperationID, "stopped"), nil
}

func (backend *fakeBackend) StartCall(_ context.Context, input vowifiipc.StartCallRequest) (vowifiipc.CallResult, error) {
	backend.record("calls/start")
	return vowifiipc.CallResult{OperationResult: backend.operation(input.OperationID, "call_started"), CallID: input.CallID}, nil
}

func (backend *fakeBackend) EndCall(_ context.Context, input vowifiipc.EndCallRequest) (vowifiipc.CallResult, error) {
	backend.record("calls/end")
	return vowifiipc.CallResult{OperationResult: backend.operation(input.OperationID, "call_ended"), CallID: input.CallID}, nil
}

func (backend *fakeBackend) SendDTMF(_ context.Context, input vowifiipc.SendDTMFRequest) (vowifiipc.CallResult, error) {
	backend.record("calls/dtmf")
	return vowifiipc.CallResult{OperationResult: backend.operation(input.OperationID, "dtmf_rtp"), CallID: input.CallID}, nil
}

func (backend *fakeBackend) AnswerIncomingCall(_ context.Context, input vowifiipc.AnswerIncomingCallRequest) (vowifiipc.CallResult, error) {
	backend.record("calls/incoming/answer")
	return vowifiipc.CallResult{OperationResult: backend.operation(input.OperationID, "active"), CallID: input.CallID}, nil
}

func (backend *fakeBackend) RejectIncomingCall(_ context.Context, input vowifiipc.RejectIncomingCallRequest) (vowifiipc.CallResult, error) {
	backend.record("calls/incoming/reject")
	return vowifiipc.CallResult{OperationResult: backend.operation(input.OperationID, "rejected"), CallID: input.CallID}, nil
}

func (backend *fakeBackend) SendMessage(_ context.Context, input vowifiipc.SendMessageRequest) (vowifiipc.MessageResult, error) {
	backend.record("messages/send")
	if backend.messageErr != nil {
		return vowifiipc.MessageResult{}, backend.messageErr
	}
	return vowifiipc.MessageResult{OperationResult: backend.operation(input.OperationID, "sent"), MessageID: input.MessageID}, nil
}

func (backend *fakeBackend) operation(id, code string) vowifiipc.OperationResult {
	return vowifiipc.OperationResult{OperationID: id, Accepted: true, Code: code, Status: backend.snapshot}
}

func (backend *fakeBackend) record(operation string) {
	backend.mu.Lock()
	backend.operations = append(backend.operations, operation)
	backend.mu.Unlock()
}

func TestHandlerRoutesAllOperationsToCurrentProvider(t *testing.T) {
	backend := newFakeBackend("generation-1")
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, "generation-1")
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	public := publicServer(handler)
	defer public.Close()
	backend.snapshot.ActiveCall = &vowifiipc.ActiveCall{CallID: "call-current", Condition: vowifiipc.CallActive}
	status, err := http.Get(public.URL + "/v1/lines/line-1/vowifi/status")
	if err != nil {
		t.Fatal(err)
	}
	defer status.Body.Close()
	if status.StatusCode != http.StatusOK {
		t.Fatalf("status route=%d", status.StatusCode)
	}
	var current vowifiipc.Snapshot
	if err := json.NewDecoder(status.Body).Decode(&current); err != nil || current.ActiveCall == nil || current.ActiveCall.CallID != "call-current" {
		t.Fatalf("status snapshot=%+v err=%v", current, err)
	}

	tests := []struct{ operation, body string }{
		{"runtime/start", `{"operation_id":"start-1"}`},
		{"runtime/stop", `{"operation_id":"stop-1"}`},
		{"calls/start", `{"operation_id":"call-start-1","call_id":"call-1","callee":"+44123","media_buffer_ms":500,"expected_card_id":"8944100000000000001"}`},
		{"calls/dtmf", `{"operation_id":"call-dtmf-1","call_id":"call-1","signal":"5","duration_ms":160}`},
		{"calls/end", `{"operation_id":"call-end-1","call_id":"call-1","reason_code":"user_hangup"}`},
		{"calls/incoming/answer", `{"operation_id":"incoming-answer-1","call_id":"incoming-1","media_buffer_ms":500}`},
		{"calls/incoming/reject", `{"operation_id":"incoming-reject-1","call_id":"incoming-2","reason_code":"user_rejected"}`},
		{"messages/send", `{"operation_id":"message-send-1","message_id":"message-1","recipient":"+44123","body":"hello"}`},
	}
	for _, test := range tests {
		response := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/"+test.operation, test.body)
		if response.status != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", test.operation, response.status, response.body)
		}
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if strings.Join(backend.operations, ",") != "runtime/start,runtime/stop,calls/start,calls/dtmf,calls/end,calls/incoming/answer,calls/incoming/reject,messages/send" {
		t.Fatalf("operations=%v", backend.operations)
	}
}

func TestHandlerPreservesProviderFailureAndRejectsInvalidOrStaleRoutes(t *testing.T) {
	backend := newFakeBackend("generation-1")
	backend.messageErr = &vowifiipc.OperationError{
		Kind: vowifiipc.ErrorNotReady, Code: "messaging_transport_unavailable", Layer: "messaging",
	}
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, "generation-1")
	handler, _ := NewHandler(directory, paidActionCatalog(), provider.Client())
	public := publicServer(handler)
	defer public.Close()

	response := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/messages/send",
		`{"operation_id":"send-1","message_id":"message-1","recipient":"+44123","body":"hello"}`)
	assertFailure(t, response, http.StatusPreconditionFailed, "messaging_transport_unavailable")
	response = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/runtime/start", `{"operation_id":"bad","extra":true}`)
	assertFailure(t, response, http.StatusBadRequest, "invalid_request")
	response = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/unknown", `{}`)
	assertFailure(t, response, http.StatusNotFound, "operation_not_found")
	response = postJSON(t, public.URL+"/v1/lines/missing/vowifi/runtime/start", `{"operation_id":"start-1"}`)
	assertFailure(t, response, http.StatusPreconditionFailed, "provider_unavailable")

	staleDirectory := mediaauth.NewProviderDirectory()
	registerProvider(t, staleDirectory, provider.URL, "generation-2")
	staleHandler, _ := NewHandler(staleDirectory, paidActionCatalog(), provider.Client())
	stalePublic := publicServer(staleHandler)
	defer stalePublic.Close()
	response = postJSON(t, stalePublic.URL+"/v1/lines/line-1/vowifi/runtime/start", `{"operation_id":"start-stale"}`)
	assertFailure(t, response, http.StatusBadGateway, "invalid_provider_response")
}

func TestOutgoingCallRequiresCurrentExpectedSIMBeforeProvider(t *testing.T) {
	backend := newFakeBackend("generation-1")
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, "generation-1")
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	public := publicServer(handler)
	defer public.Close()

	missing := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/calls/start",
		`{"operation_id":"missing-card","call_id":"call-1","callee":"+44123","media_buffer_ms":500}`)
	assertFailure(t, missing, http.StatusBadRequest, "invalid_request")
	mismatch := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/calls/start",
		`{"operation_id":"wrong-card","call_id":"call-2","callee":"+44123","media_buffer_ms":500,"expected_card_id":"8944100000000000999"}`)
	assertFailure(t, mismatch, http.StatusConflict, "paid_action_card_mismatch")

	staleProviderDirectory := mediaauth.NewProviderDirectory()
	if err := staleProviderDirectory.Replace(mediaauth.Provider{
		LineID: "line-1", ProviderID: "provider-1", Generation: "stale-card-generation",
		CardID: "8944100000000000999", BaseURL: "ws" + strings.TrimPrefix(provider.URL, "http"), Token: testToken,
	}); err != nil {
		t.Fatal(err)
	}
	staleHandler, err := NewHandler(staleProviderDirectory, paidActionCatalog(), provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	stalePublic := publicServer(staleHandler)
	defer stalePublic.Close()
	staleBinding := postJSON(t, stalePublic.URL+"/v1/lines/line-1/vowifi/calls/start",
		`{"operation_id":"stale-provider","call_id":"call-3","callee":"+44123","media_buffer_ms":500,"expected_card_id":"8944100000000000001"}`)
	assertFailure(t, staleBinding, http.StatusConflict, "paid_action_card_mismatch")

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.operations) != 0 {
		t.Fatalf("unsafe call reached Provider: %v", backend.operations)
	}
}

func TestHandlerPersistsLifecycleIntentBeforeProviderAction(t *testing.T) {
	backend := newFakeBackend("generation-1")
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, "generation-1")
	writer := &fakeIntentWriter{lineEnabled: true}
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client(), WithRuntimeIntent(writer))
	if err != nil {
		t.Fatal(err)
	}
	public := publicServer(handler)
	defer public.Close()

	response := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/runtime/start", `{"operation_id":"start-intent"}`)
	if response.status != http.StatusOK {
		t.Fatalf("start status=%d body=%s", response.status, response.body)
	}
	response = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/runtime/stop", `{"operation_id":"stop-intent"}`)
	if response.status != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", response.status, response.body)
	}
	writer.mu.Lock()
	writes := append([]bool(nil), writer.writes...)
	writer.mu.Unlock()
	if len(writes) != 2 || !writes[0] || writes[1] {
		t.Fatalf("intent writes=%v", writes)
	}
	response = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/runtime/start", `{"operation_id":"invalid-intent","extra":true}`)
	assertFailure(t, response, http.StatusBadRequest, "invalid_request")
	writer.mu.Lock()
	if len(writer.writes) != 2 {
		writer.mu.Unlock()
		t.Fatalf("invalid request wrote intent: %v", writer.writes)
	}
	writer.mu.Unlock()

	disabledWriter := &fakeIntentWriter{lineEnabled: false}
	disabled, err := NewHandler(directory, paidActionCatalog(), provider.Client(), WithRuntimeIntent(disabledWriter))
	if err != nil {
		t.Fatal(err)
	}
	disabledPublic := publicServer(disabled)
	defer disabledPublic.Close()
	response = postJSON(t, disabledPublic.URL+"/v1/lines/line-1/vowifi/runtime/start", `{"operation_id":"disabled-start"}`)
	assertFailure(t, response, http.StatusPreconditionFailed, "line_disabled")
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if strings.Join(backend.operations, ",") != "runtime/start,runtime/stop" {
		t.Fatalf("disabled line reached Provider: %v", backend.operations)
	}
}

func providerServer(t *testing.T, backend vowifiipc.Backend) *httptest.Server {
	t.Helper()
	api, err := vowifiipc.NewAPI(backend, testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	return server
}

func registerProvider(t *testing.T, directory *mediaauth.ProviderDirectory, httpURL, generation string) {
	t.Helper()
	err := directory.Replace(mediaauth.Provider{
		LineID: "line-1", ProviderID: "provider-1", Generation: generation,
		CardID: "8944100000000000001", BaseURL: "ws" + strings.TrimPrefix(httpURL, "http"), Token: testToken,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publicServer(handler http.Handler) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /v1/lines/{lineID}/vowifi/{operation...}", handler)
	mux.Handle("POST /v1/lines/{lineID}/vowifi/{operation...}", handler)
	return httptest.NewServer(mux)
}

type responseRecord struct {
	status int
	body   []byte
}

func postJSON(t *testing.T, url, body string) responseRecord {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	result, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	wire, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	return responseRecord{status: result.StatusCode, body: wire}
}

func assertFailure(t *testing.T, response responseRecord, status int, code string) {
	t.Helper()
	var failure vowifiipc.OperationError
	err := json.Unmarshal(response.body, &failure)
	if err != nil || response.status != status || failure.Code != code {
		t.Fatalf("status=%d failure=%+v decode=%v body=%s", response.status, failure, err, response.body)
	}
}
