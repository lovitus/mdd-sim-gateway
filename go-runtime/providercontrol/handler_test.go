package providercontrol

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

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeBackend struct {
	mu           sync.Mutex
	snapshot     vowifiipc.Snapshot
	operations   []string
	stopRequests []vowifiipc.LifecycleRequest
	messageErr   error
}

type fakeIntentRequester struct {
	mu       sync.Mutex
	requests []runtimeMutation
	snapshot vowifiipc.Snapshot
	err      error
}

type fakeCatalog struct {
	line linecatalog.Line
	err  error
}

type fakeCardRoutes struct{ err error }

func (routes fakeCardRoutes) ResolveCardRoute(cardID string) (agentlink.CardRouteTarget, error) {
	if routes.err != nil {
		return agentlink.CardRouteTarget{}, routes.err
	}
	return agentlink.CardRouteTarget{AgentID: "agent-1", ProcessGeneration: "process-1",
		SessionGeneration: "card-1", CardID: cardID, Kind: "reader"}, nil
}

type fakeAllowanceAuthorizer struct {
	err   error
	calls int
}

func (authorizer *fakeAllowanceAuthorizer) AuthorizeDispatch(string, string, string, string, string, string, string, string) error {
	authorizer.calls++
	return authorizer.err
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
	return fakeCatalog{line: linecatalog.Line{ID: "line-1", CardID: "8944100000000000001", Enabled: true}}
}

func (requester *fakeIntentRequester) RequestIntent(_ context.Context, _ string, enabled bool, operationID string) (vowifiipc.OperationResult, error) {
	requester.mu.Lock()
	defer requester.mu.Unlock()
	requester.requests = append(requester.requests, runtimeMutation{operationID: operationID, enabled: enabled})
	if requester.err != nil {
		return vowifiipc.OperationResult{}, requester.err
	}
	code := "stopped"
	if enabled {
		code = "started"
	}
	return vowifiipc.OperationResult{
		OperationID: operationID, Accepted: true, Code: code, Status: requester.snapshot,
	}, nil
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
	backend.mu.Lock()
	backend.stopRequests = append(backend.stopRequests, input)
	backend.mu.Unlock()
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

func (backend *fakeBackend) Register(_ context.Context, input vowifiipc.RegisterRequest) (vowifiipc.OperationResult, error) {
	backend.record("register")
	return backend.operation(input.OperationID, "ims_registered"), nil
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
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client(), WithCardRouteResolver(fakeCardRoutes{}))
	if err != nil {
		t.Fatal(err)
	}
	requester := &fakeIntentRequester{snapshot: backend.snapshot}
	if err := handler.BindRuntimeIntentRequester(requester); err != nil {
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
		{"register", `{"operation_id":"register-1","expected_card_id":"8944100000000000001"}`},
		{"calls/start", `{"operation_id":"call-start-1","call_id":"call-1","callee":"+44123","media_buffer_ms":500,"expected_card_id":"8944100000000000001"}`},
		{"calls/dtmf", `{"operation_id":"call-dtmf-1","call_id":"call-1","signal":"5","duration_ms":160}`},
		{"calls/end", `{"operation_id":"call-end-1","call_id":"call-1","reason_code":"user_hangup"}`},
		{"calls/incoming/answer", `{"operation_id":"incoming-answer-1","call_id":"incoming-1","media_buffer_ms":500}`},
		{"calls/incoming/reject", `{"operation_id":"incoming-reject-1","call_id":"incoming-2","reason_code":"user_rejected"}`},
		{"messages/send", `{"operation_id":"message-send-1","message_id":"message-1","recipient":"+44123","body":"hello","expected_card_id":"8944100000000000001"}`},
	}
	for _, test := range tests {
		response := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/"+test.operation, test.body)
		if response.status != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", test.operation, response.status, response.body)
		}
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if strings.Join(backend.operations, ",") != "register,calls/start,calls/dtmf,calls/end,calls/incoming/answer,calls/incoming/reject,messages/send" {
		t.Fatalf("operations=%v", backend.operations)
	}
	requester.mu.Lock()
	defer requester.mu.Unlock()
	if len(requester.requests) != 2 || !requester.requests[0].enabled || requester.requests[1].enabled {
		t.Fatalf("runtime requests=%+v", requester.requests)
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
	handler, _ := NewHandler(directory, paidActionCatalog(), provider.Client(), WithCardRouteResolver(fakeCardRoutes{}))
	public := publicServer(handler)
	defer public.Close()

	response := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/messages/send",
		`{"operation_id":"send-1","message_id":"message-1","recipient":"+44123","body":"hello","expected_card_id":"8944100000000000001"}`)
	assertFailure(t, response, http.StatusPreconditionFailed, "messaging_transport_unavailable")
	response = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/runtime/start", `{"operation_id":"bad","extra":true}`)
	assertFailure(t, response, http.StatusBadRequest, "invalid_request")
	response = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/unknown", `{}`)
	assertFailure(t, response, http.StatusNotFound, "operation_not_found")
	missingStatus, err := http.Get(public.URL + "/v1/lines/missing/vowifi/status")
	if err != nil {
		t.Fatal(err)
	}
	missingBody, _ := io.ReadAll(missingStatus.Body)
	_ = missingStatus.Body.Close()
	assertFailure(t, responseRecord{status: missingStatus.StatusCode, body: missingBody}, http.StatusPreconditionFailed, "provider_unavailable")

	staleDirectory := mediaauth.NewProviderDirectory()
	registerProvider(t, staleDirectory, provider.URL, "generation-2")
	staleHandler, _ := NewHandler(staleDirectory, paidActionCatalog(), provider.Client())
	stalePublic := publicServer(staleHandler)
	defer stalePublic.Close()
	staleStatus, err := http.Get(stalePublic.URL + "/v1/lines/line-1/vowifi/status")
	if err != nil {
		t.Fatal(err)
	}
	staleBody, _ := io.ReadAll(staleStatus.Body)
	_ = staleStatus.Body.Close()
	assertFailure(t, responseRecord{status: staleStatus.StatusCode, body: staleBody}, http.StatusBadGateway, "invalid_provider_response")
}

func TestMessageSendRequiresCurrentExpectedCardAndLiveCardRoute(t *testing.T) {
	backend := newFakeBackend("generation-1")
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, "generation-1")
	routes := fakeCardRoutes{err: agentlink.ErrCardAmbiguous}
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client(), WithCardRouteResolver(routes))
	if err != nil {
		t.Fatal(err)
	}
	public := publicServer(handler)
	defer public.Close()
	response := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/messages/send",
		`{"operation_id":"send-route","message_id":"message-route","recipient":"+44123","body":"hello","expected_card_id":"8944100000000000001"}`)
	assertFailure(t, response, http.StatusPreconditionFailed, "card_route_unavailable")
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.operations) != 0 {
		t.Fatalf("provider received paid operation despite ambiguous route: %v", backend.operations)
	}
}

func TestVerifyMessageRouteRequiresProviderMessagingAndLiveCardRoute(t *testing.T) {
	backend := newFakeBackend("generation-1")
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, "generation-1")
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client(),
		WithCardRouteResolver(fakeCardRoutes{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.VerifyMessageRoute(context.Background(), "line-1", "8944100000000000001"); err != nil {
		t.Fatalf("ready route err=%v", err)
	}
	backend.snapshot.Messaging = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "blocked"}
	if err := handler.VerifyMessageRoute(context.Background(), "line-1", "8944100000000000001"); err == nil {
		t.Fatal("blocked messaging route was accepted")
	}
	if err := handler.VerifyMessageRoute(context.Background(), "line-1", "8944100000000000999"); err == nil {
		t.Fatal("wrong card route was accepted")
	}
}

func TestAllowanceMessageDispatchIsRevokedAtFinalPaidBoundary(t *testing.T) {
	backend := newFakeBackend("generation-1")
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, "generation-1")
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client(), WithCardRouteResolver(fakeCardRoutes{}))
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &fakeAllowanceAuthorizer{err: errors.New("query closed")}
	if err := handler.BindAllowanceDispatchAuthorizer(authorizer); err != nil {
		t.Fatal(err)
	}
	public := publicServer(handler)
	defer public.Close()
	response := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/messages/send",
		`{"operation_id":"allowance-send","message_id":"allowance-message","recipient":"6700","body":"BAL","expected_card_id":"8944100000000000001","allowance_query_id":"allowance-query"}`)
	assertFailure(t, response, http.StatusConflict, "allowance_query_changed")
	if authorizer.calls != 1 {
		t.Fatalf("authorize calls=%d", authorizer.calls)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.operations) != 0 {
		t.Fatalf("closed allowance dispatch reached Provider: %v", backend.operations)
	}
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

func TestPublicStatusRejectsWrongOrEmptyProviderCardBinding(t *testing.T) {
	for _, cardID := range []string{"", "8944100000000000999"} {
		t.Run(cardID, func(t *testing.T) {
			backend := newFakeBackend("generation-1")
			provider := providerServer(t, backend)
			directory := mediaauth.NewProviderDirectory()
			if err := directory.Replace(mediaauth.Provider{
				LineID: "line-1", ProviderID: "provider-1", Generation: "generation-1",
				CardID: cardID, BaseURL: "ws" + strings.TrimPrefix(provider.URL, "http"), Token: testToken,
			}); err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler(directory, paidActionCatalog(), provider.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := handler.Observe(t.Context(), "line-1"); err != nil {
				t.Fatalf("raw observation must remain available for cleanup: %v", err)
			}
			if _, err := handler.Status(t.Context(), "line-1"); operationCode(err) != "provider_card_mismatch" {
				t.Fatalf("direct status err=%v", err)
			}
			public := publicServer(handler)
			defer public.Close()
			response, err := http.Get(public.URL + "/v1/lines/line-1/vowifi/status")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			assertFailure(t, responseRecord{status: response.StatusCode, body: body},
				http.StatusConflict, "provider_card_mismatch")
		})
	}
}

func TestObservedProviderFenceCannotReachReplacement(t *testing.T) {
	firstBackend := newFakeBackend("generation-1")
	firstServer := providerServer(t, firstBackend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, firstServer.URL, "generation-1")
	handler, err := NewHandler(directory, paidActionCatalog(), firstServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, fence, err := handler.Observe(t.Context(), "line-1")
	if err != nil {
		t.Fatal(err)
	}
	secondBackend := newFakeBackend("generation-2")
	secondServer := providerServer(t, secondBackend)
	if err := directory.Replace(mediaauth.Provider{
		LineID: "line-1", ProviderID: "provider-1", Generation: "generation-2",
		CardID: "8944100000000000001", BaseURL: "ws" + strings.TrimPrefix(secondServer.URL, "http"), Token: testToken,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = handler.Start(t.Context(), "line-1", fence, vowifiipc.LifecycleRequest{OperationID: "stale-plan"})
	if !errors.Is(err, mediaauth.ErrProviderFenceConflict) {
		t.Fatalf("stale fence error=%v", err)
	}
	secondBackend.mu.Lock()
	defer secondBackend.mu.Unlock()
	if len(secondBackend.operations) != 0 {
		t.Fatalf("stale plan reached replacement Provider: %v", secondBackend.operations)
	}
}

func TestRecoveryStopCapabilityGatesMixedProviderVersions(t *testing.T) {
	t.Run("new_core_old_provider", func(t *testing.T) {
		var mu sync.Mutex
		stopRequests := 0
		oldProvider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case "/healthz":
				writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
			case "/v1/runtime/stop":
				mu.Lock()
				stopRequests++
				mu.Unlock()
				writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "unexpected_stop"})
			default:
				http.NotFound(response, request)
			}
		}))
		defer oldProvider.Close()
		directory := mediaauth.NewProviderDirectory()
		provider := mediaauth.Provider{
			LineID: "line-1", ProviderID: "provider-1", Generation: "old-generation",
			CardID: "8944100000000000001", BaseURL: "ws" + strings.TrimPrefix(oldProvider.URL, "http"), Token: testToken,
		}
		if err := directory.Replace(provider); err != nil {
			t.Fatal(err)
		}
		handler, err := NewHandler(directory, paidActionCatalog(), oldProvider.Client())
		if err != nil {
			t.Fatal(err)
		}
		_, err = handler.Stop(t.Context(), "line-1", provider.Fence(), vowifiipc.LifecycleRequest{
			OperationID: "mixed-recovery-stop", RequireIdle: true,
		})
		if operationCode(err) != "recovery_stop_unsupported" {
			t.Fatalf("old Provider recovery stop err=%v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if stopRequests != 0 {
			t.Fatalf("old Provider received %d recovery stop requests", stopRequests)
		}
	})

	t.Run("new_provider_preserves_old_operations_and_allows_recovery", func(t *testing.T) {
		backend := newFakeBackend("generation-1")
		provider := providerServer(t, backend)
		directory := mediaauth.NewProviderDirectory()
		registerProvider(t, directory, provider.URL, "generation-1")
		handler, err := NewHandler(directory, paidActionCatalog(), provider.Client())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handler.Status(t.Context(), "line-1"); err != nil {
			t.Fatal(err)
		}
		_, fence, err := handler.Observe(t.Context(), "line-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handler.Start(t.Context(), "line-1", fence, vowifiipc.LifecycleRequest{OperationID: "old-core-start"}); err != nil {
			t.Fatal(err)
		}
		if _, err := handler.Stop(t.Context(), "line-1", fence, vowifiipc.LifecycleRequest{OperationID: "old-core-stop"}); err != nil {
			t.Fatal(err)
		}
		if _, err := handler.Stop(t.Context(), "line-1", fence, vowifiipc.LifecycleRequest{
			OperationID: "new-core-recovery-stop", RequireIdle: true,
		}); err != nil {
			t.Fatal(err)
		}
		backend.mu.Lock()
		defer backend.mu.Unlock()
		if strings.Join(backend.operations, ",") != "runtime/start,runtime/stop,runtime/stop" ||
			len(backend.stopRequests) != 2 || backend.stopRequests[0].RequireIdle || !backend.stopRequests[1].RequireIdle {
			t.Fatalf("operations=%v stop requests=%+v", backend.operations, backend.stopRequests)
		}
	})
}

func TestHandlerRoutesLifecycleOnlyThroughBoundIntentRequester(t *testing.T) {
	backend := newFakeBackend("generation-1")
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, "generation-1")
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	requester := &fakeIntentRequester{snapshot: backend.snapshot}
	if err := handler.BindRuntimeIntentRequester(requester); err != nil {
		t.Fatal(err)
	}
	if err := handler.BindRuntimeIntentRequester(requester); err == nil {
		t.Fatal("second runtime requester binding succeeded")
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
	requester.mu.Lock()
	requests := append([]runtimeMutation(nil), requester.requests...)
	requester.mu.Unlock()
	if len(requests) != 2 || !requests[0].enabled || requests[0].operationID != "start-intent" ||
		requests[1].enabled || requests[1].operationID != "stop-intent" {
		t.Fatalf("intent requests=%+v", requests)
	}
	response = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/runtime/start", `{"operation_id":"invalid-intent","extra":true}`)
	assertFailure(t, response, http.StatusBadRequest, "invalid_request")
	for _, operation := range []string{"start", "stop"} {
		response = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/runtime/"+operation,
			`{"operation_id":"private-recovery-flag","require_idle":true}`)
		assertFailure(t, response, http.StatusBadRequest, "invalid_request")
	}
	requester.mu.Lock()
	if len(requester.requests) != 2 {
		requester.mu.Unlock()
		t.Fatalf("invalid request reached requester: %+v", requester.requests)
	}
	requester.mu.Unlock()

	disabled, err := NewHandler(directory, paidActionCatalog(), provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	disabledRequester := &fakeIntentRequester{err: &vowifiipc.OperationError{
		Kind: vowifiipc.ErrorNotReady, Code: "line_disabled", Layer: "intent",
	}}
	if err := disabled.BindRuntimeIntentRequester(disabledRequester); err != nil {
		t.Fatal(err)
	}
	disabledPublic := publicServer(disabled)
	defer disabledPublic.Close()
	response = postJSON(t, disabledPublic.URL+"/v1/lines/line-1/vowifi/runtime/start", `{"operation_id":"disabled-start"}`)
	assertFailure(t, response, http.StatusPreconditionFailed, "line_disabled")
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.operations) != 0 {
		t.Fatalf("lifecycle request reached Provider directly: %v", backend.operations)
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

func operationCode(err error) string {
	var failure *vowifiipc.OperationError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}
