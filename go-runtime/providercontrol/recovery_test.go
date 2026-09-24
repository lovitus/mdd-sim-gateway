package providercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type receiptBackend struct {
	*fakeBackend
	receipt vowifiipc.CallReceipt
	reads   int
}

type recoveryBrowserAuth string

func (a recoveryBrowserAuth) AuthorizeBrowserMutation(*http.Request) (string, error) {
	return string(a), nil
}

func TestBoundMediaSessionCannotDispatchAnotherCallOrOperation(t *testing.T) {
	store, err := callhistory.Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := callhistory.RecoveryRecord{LineID: "line-1", Transport: "vowifi", CardID: "8944100000000000001", CallID: "original-call", OperationID: "original-start", SessionID: "original-session", Subject: "original-login", ProviderID: "provider-1", ProviderGeneration: "generation-1", CreatedAt: time.Now()}
	if err = store.BindRecovery(record, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	backend := newFakeBackend(record.ProviderGeneration)
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, record.ProviderGeneration)
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client(), WithCallRecorder(store), WithBrowserAuthorization(recoveryBrowserAuth(record.Subject)))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"call", "operation", "subject", "session"} {
		call, operation, session, subject := record.CallID, record.OperationID, record.SessionID, record.Subject
		switch scenario {
		case "call":
			call = "other-call"
		case "operation":
			operation = "other-operation"
		case "session":
			session = "other-session"
		case "subject":
			subject = "other-login"
		}
		handler.browserAuth = recoveryBrowserAuth(subject)
		payload, _ := json.Marshal(map[string]any{"operation_id": operation, "call_id": call, "media_session_id": session, "callee": "+15550100123", "media_buffer_ms": 500, "expected_card_id": record.CardID})
		r := httptest.NewRequest("POST", "/v1/lines/line-1/vowifi/calls/start", bytes.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("lineID", record.LineID)
		r.SetPathValue("operation", "calls/start")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if response.Code != 409 || !strings.Contains(response.Body.String(), "call_recovery_binding_mismatch") || len(backend.operations) != 0 {
			t.Fatalf("%s dispatched: %d %s operations=%v", scenario, response.Code, response.Body.String(), backend.operations)
		}
	}
}

func (b *receiptBackend) CallReceipt(_ context.Context, r vowifiipc.CallReceiptRequest) (vowifiipc.CallReceipt, error) {
	b.reads++
	if r != b.receipt.CallReceiptRequest {
		return vowifiipc.CallReceipt{}, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotFound, Code: "call_terminal_unavailable", Layer: "call"}
	}
	return b.receipt, nil
}

func TestProviderRecoveryPreservesTerminalOutcomeAcrossProviderRestart(t *testing.T) {
	for _, scenario := range []struct{ name, source, outcome string }{
		{name: "natural end", source: "carrier_bye", outcome: "ended"},
		{name: "definitive rejection", source: "confirmed_rejected", outcome: "rejected"},
	} {
		t.Run(scenario.name, func(t *testing.T) { assertProviderRecoveryTerminalOutcome(t, scenario.source, scenario.outcome) })
	}
}

func assertProviderRecoveryTerminalOutcome(t *testing.T, source, outcome string) {
	t.Helper()
	store, err := callhistory.Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := strings.Repeat("a", 64)
	record := callhistory.RecoveryRecord{LineID: "line-1", Transport: "vowifi", CardID: "8944100000000000001", CallID: "original-call", OperationID: "original-start", SessionID: "original-session", Subject: "original-login", ProviderID: "provider-1", ProviderGeneration: "generation-1", CreatedAt: time.Now().Add(-time.Minute)}
	if err = store.BindRecovery(record, key); err != nil {
		t.Fatal(err)
	}
	backend := &receiptBackend{fakeBackend: newFakeBackend("generation-2"), receipt: vowifiipc.CallReceipt{CallReceiptRequest: vowifiipc.CallReceiptRequest{CallID: record.CallID, OperationID: record.OperationID, SessionID: record.SessionID}, LineID: record.LineID, ProviderID: record.ProviderID, ProcessGeneration: record.ProviderGeneration, ConfirmedAt: time.Now(), Source: source, Outcome: outcome}}
	api, err := vowifiipc.NewAPI(backend, testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(api)
	defer provider.Close()
	directory := mediaauth.NewProviderDirectory()
	if err = directory.Replace(mediaauth.Provider{LineID: record.LineID, ProviderID: record.ProviderID, Generation: "generation-2", CardID: record.CardID, BaseURL: "ws" + strings.TrimPrefix(provider.URL, "http"), Token: testToken}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client(), WithCallRecorder(store))
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(capability string) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]string{"action": "status", "call_id": record.CallID, "operation_id": record.OperationID, "recovery_key": capability})
		r := httptest.NewRequest("POST", "/v1/lines/line-1/vowifi/calls/recovery", bytes.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("lineID", record.LineID)
		r.SetPathValue("operation", "calls/recovery")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if response := invoke(strings.Repeat("b", 64)); response.Code != 404 || backend.reads != 0 {
		t.Fatal("wrong grant queried provider")
	}
	response := invoke(key)
	var result map[string]any
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result["terminal_confirmed"] != true || result["terminal_outcome"] != outcome || backend.reads != 1 {
		t.Fatalf("%d %s reads=%d", response.Code, response.Body.String(), backend.reads)
	}
	if len(backend.operations) != 0 {
		t.Fatal("natural terminal lookup sent a mutation")
	}
	confirmed, err := store.ReadRecovery(record.LineID, record.Transport, record.CallID, record.OperationID, key)
	if err != nil || confirmed.TerminalOutcome != outcome {
		t.Fatalf("durable terminal outcome = %q, err=%v", confirmed.TerminalOutcome, err)
	}
	if response = invoke(key); response.Code != 200 || backend.reads != 1 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result["terminal_outcome"] != outcome {
		t.Fatal("stored terminal re-queried provider")
	}
}

type recoveredEndBackend struct {
	*fakeBackend
	receipt        vowifiipc.CallReceipt
	complete       bool
	endRequests    []vowifiipc.EndCallRequest
	receiptFailure *vowifiipc.OperationError
}

func (b *recoveredEndBackend) CallReceipt(_ context.Context, input vowifiipc.CallReceiptRequest) (vowifiipc.CallReceipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.receiptFailure != nil {
		return vowifiipc.CallReceipt{}, b.receiptFailure
	}
	if input != b.receipt.CallReceiptRequest {
		return vowifiipc.CallReceipt{}, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotFound, Code: "call_terminal_unavailable", Layer: "call"}
	}
	if !b.complete {
		return vowifiipc.CallReceipt{}, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "call_active", Layer: "call"}
	}
	return b.receipt, nil
}
func (b *recoveredEndBackend) EndCall(_ context.Context, input vowifiipc.EndCallRequest) (vowifiipc.CallResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if input.CallID != b.receipt.CallID || input.ExpectedStartOperationID != b.receipt.OperationID || input.OperationID != "original-end" {
		return vowifiipc.CallResult{}, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "call_identity_conflict", Layer: "call"}
	}
	b.endRequests = append(b.endRequests, input)
	b.complete = true
	b.receipt.ConfirmedAt = time.Now().UTC()
	return vowifiipc.CallResult{OperationResult: b.operation(input.OperationID, "ended"), CallID: input.CallID}, nil
}
func TestProviderRecoveryEndKeepsOriginalIdentityAndDoesNotExecuteTwice(t *testing.T) {
	store, err := callhistory.Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := strings.Repeat("a", 64)
	record := callhistory.RecoveryRecord{LineID: "line-1", Transport: "vowifi", CardID: "8944100000000000001", CallID: "original-call", OperationID: "original-start", SessionID: "original-session", Subject: "original-login", ProviderID: "provider-1", ProviderGeneration: "generation-1", CreatedAt: time.Now().Add(-time.Minute)}
	if err = store.BindRecovery(record, key); err != nil {
		t.Fatal(err)
	}
	backend := &recoveredEndBackend{fakeBackend: newFakeBackend(record.ProviderGeneration), receipt: vowifiipc.CallReceipt{CallReceiptRequest: vowifiipc.CallReceiptRequest{CallID: record.CallID, OperationID: record.OperationID, SessionID: record.SessionID}, LineID: record.LineID, ProviderID: record.ProviderID, ProcessGeneration: record.ProviderGeneration, Source: "confirmed_end"}}
	api, err := vowifiipc.NewAPI(backend, testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var invalidReceiptResponse atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fault := invalidReceiptResponse.Load(); r.URL.Path == "/v1/calls/receipt" && fault != 0 {
			status, kind := http.StatusUnauthorized, vowifiipc.ErrorNotReady
			if fault == 2 {
				status, kind = http.StatusPreconditionFailed, vowifiipc.ErrorFailed
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(vowifiipc.OperationError{Kind: kind, Code: "call_active", Layer: "call"})
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer provider.Close()
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, record.ProviderGeneration)
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client(), WithCallRecorder(store), WithBrowserAuthorization(recoveryBrowserAuth("new-login")))
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(capability string) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]string{"action": "end", "call_id": record.CallID, "operation_id": record.OperationID, "recovery_key": capability, "end_operation_id": "original-end"})
		r := httptest.NewRequest("POST", "/v1/lines/line-1/vowifi/calls/recovery", bytes.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("lineID", record.LineID)
		r.SetPathValue("operation", "calls/recovery")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := invoke(strings.Repeat("b", 64)); w.Code != 404 {
		t.Fatal("wrong grant accepted", w.Code)
	}
	assertNotControlled := func(response *httptest.ResponseRecorder) {
		t.Helper()
		var value map[string]any
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || value["terminal_confirmed"] != false {
			t.Fatal("non-active proof became terminal", response.Code)
		}
		backend.mu.Lock()
		count := len(backend.endRequests)
		backend.mu.Unlock()
		if count != 0 {
			t.Fatal("non-active proof dispatched end")
		}
	}
	for _, failure := range []*vowifiipc.OperationError{
		{Kind: vowifiipc.ErrorFailed, Code: "call_active", Layer: "call"},
		{Kind: vowifiipc.ErrorNotReady, Code: "call_active", Layer: "runtime"},
		{Kind: vowifiipc.ErrorNotReady, Code: "call_terminal_unavailable", Layer: "call"},
	} {
		backend.mu.Lock()
		backend.receiptFailure = failure
		backend.mu.Unlock()
		assertNotControlled(invoke(key))
	}
	backend.mu.Lock()
	backend.receiptFailure = nil
	backend.mu.Unlock()
	for _, fault := range []int32{1, 2} {
		invalidReceiptResponse.Store(fault)
		assertNotControlled(invoke(key))
	}
	invalidReceiptResponse.Store(0)
	for i := 0; i < 2; i++ {
		w := invoke(key)
		var result map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["terminal_confirmed"] != true || result["session_id"] != record.SessionID {
			t.Fatalf("end/receipt %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.endRequests) != 1 || len(backend.operations) != 0 {
		t.Fatal("original end did not remain one execution", len(backend.endRequests), backend.operations)
	}
	if backend.endRequests[0].ExpectedStartOperationID != record.OperationID || backend.endRequests[0].ReasonCode != "user_recovery_hangup" {
		t.Fatal("original dispatch identity changed")
	}
	saved, err := store.ReadRecovery(record.LineID, record.Transport, record.CallID, record.OperationID, key)
	if err != nil || saved.Subject != record.Subject || saved.TerminalAt.IsZero() {
		t.Fatal("original binding was not retained", err)
	}
}
