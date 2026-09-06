package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type pinRuntimeStub struct {
	request agentlink.SIMPINRequest
	result  agentlink.SIMPINResponse
	err     error
	calls   int
}

func (stub *pinRuntimeStub) ResolveCardRoute(string) (agentlink.CardRouteTarget, error) {
	return agentlink.CardRouteTarget{}, errTarget
}
func (stub *pinRuntimeStub) ResolveModemTargetForAction(_, _ string, _ agentlink.ModemAction) (agentlink.ModemTarget, error) {
	return agentlink.ModemTarget{AgentID: "agent-1", ProcessGeneration: "process-1", AttachmentID: "attach-1", EquipmentID: "862547055201716", CardID: "89010000000000000001", SIMSessionGeneration: "session-1"}, nil
}
func (stub *pinRuntimeStub) ExecuteSIMPIN(_ context.Context, agentID, process string, request agentlink.SIMPINRequest) (agentlink.SIMPINResponse, error) {
	if agentID != "agent-1" || process != "process-1" {
		return agentlink.SIMPINResponse{}, errTarget
	}
	stub.calls++
	stub.request = request
	if stub.err != nil {
		return stub.result, stub.err
	}
	result := stub.result
	result.OperationID, result.CardID = request.OperationID, request.CardID
	result.ReaderName, result.AttachmentID, result.EquipmentID = request.ReaderName, request.AttachmentID, request.EquipmentID
	result.SIMSessionGeneration, result.Action = request.SIMSessionGeneration, request.Action
	if result.State == "" {
		result.State = "verified"
		if request.Action == agentlink.SIMPINStatus {
			attempts := uint32(3)
			result.State, result.AttemptsRemaining = "pin_required", &attempts
		}
	}
	return result, nil
}

func newPINTestHandler(t *testing.T, stub *pinRuntimeStub) (*SIMPINHandler, *linecatalog.Store) {
	t.Helper()
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewSIMPINHandler(stub, store)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return handler, store
}

func primePINStatus(t *testing.T, handler *SIMPINHandler, operationID string) {
	t.Helper()
	payload := `{"operation_id":"` + operationID + `","card_id":"89010000000000000001","equipment_id":"862547055201716","action":"status"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(payload)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"pin_required"`) ||
		!strings.Contains(response.Body.String(), `"attempts_remaining":3`) {
		t.Fatalf("status preflight code=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSIMPINHandlerForwardsExactModemFence(t *testing.T) {
	stub := &pinRuntimeStub{}
	handler, store := newPINTestHandler(t, stub)
	defer store.Close()
	primePINStatus(t, handler, "pin-status-operation-1")
	request := httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(`{"operation_id":"pin-operation-1","card_id":"89010000000000000001","equipment_id":"862547055201716","action":"verify","pin":"1234","preflight_operation_id":"pin-status-operation-1"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.request.AttachmentID != "attach-1" || stub.request.SIMSessionGeneration != "session-1" || strings.Contains(response.Body.String(), "1234") {
		t.Fatalf("status=%d body=%s request=%+v", response.Code, response.Body.String(), stub.request)
	}
}

func TestSIMPINHandlerRejectsUnsafeRequestBeforeResolution(t *testing.T) {
	handler, store := newPINTestHandler(t, &pinRuntimeStub{})
	defer store.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(`{"operation_id":"pin-operation-1","card_id":"89010000000000000001","equipment_id":"862547055201716","reader_name":"also-reader","action":"verify","pin":"1234"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_sim_pin_request") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSIMPINHandlerReceiptReplaysWithoutSecondHardwareAction(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &pinRuntimeStub{}
	handler, err := NewSIMPINHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	primePINStatus(t, handler, "pin-status-replay")
	payload := `{"operation_id":"pin-operation-1","card_id":"89010000000000000001","equipment_id":"862547055201716","action":"verify","pin":"1234","preflight_operation_id":"pin-status-replay"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(payload))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}
	first := stub.request
	firstCalls := stub.calls
	secondOperation := strings.Replace(payload, "pin-operation-1", "pin-operation-2", 1)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(secondOperation)))
	if response.Code != http.StatusPreconditionFailed || stub.calls != firstCalls {
		t.Fatalf("reused preflight status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(payload))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.request != first || strings.Contains(response.Body.String(), "1234") {
		t.Fatalf("replay status=%d body=%s", response.Code, response.Body.String())
	}
	conflict := strings.Replace(payload, "1234", "5678", 1)
	request = httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(conflict))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "operation_id_reused") {
		t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSIMPINVerifyRequiresFreshStatusWithMoreThanTwoAttempts(t *testing.T) {
	attempts := uint32(2)
	stub := &pinRuntimeStub{result: agentlink.SIMPINResponse{State: "pin_required", AttemptsRemaining: &attempts}}
	handler, store := newPINTestHandler(t, stub)
	defer store.Close()
	statusPayload := `{"operation_id":"pin-status-low","card_id":"89010000000000000001","equipment_id":"862547055201716","action":"status"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(statusPayload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", response.Code, response.Body.String())
	}
	verifyPayload := `{"operation_id":"pin-verify-low","card_id":"89010000000000000001","equipment_id":"862547055201716","action":"verify","pin":"1234","preflight_operation_id":"pin-status-low"}`
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(verifyPayload)))
	if response.Code != http.StatusPreconditionFailed || stub.calls != 1 || strings.Contains(response.Body.String(), "1234") {
		t.Fatalf("verify code=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
}

func TestSIMPINTransportLossRecordsUnknownWithoutRetry(t *testing.T) {
	stub := &pinRuntimeStub{}
	handler, store := newPINTestHandler(t, stub)
	defer store.Close()
	primePINStatus(t, handler, "pin-status-transport")
	stub.err = context.DeadlineExceeded
	payload := `{"operation_id":"pin-verify-transport","card_id":"89010000000000000001","equipment_id":"862547055201716","action":"verify","pin":"1234","preflight_operation_id":"pin-status-transport"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(payload)))
	receipt, found, err := store.GetOperation("pin-verify-transport")
	if response.Code != http.StatusAccepted || err != nil || !found ||
		receipt.Kind != linecatalog.OperationSIMPIN || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("status=%d receipt=%+v found=%t err=%v body=%s", response.Code, receipt, found, err, response.Body.String())
	}
}
