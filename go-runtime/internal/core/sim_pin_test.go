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

type pinRuntimeStub struct{ request agentlink.SIMPINRequest }

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
	stub.request = request
	return agentlink.SIMPINResponse{OperationID: request.OperationID, CardID: request.CardID, EquipmentID: request.EquipmentID, AttachmentID: request.AttachmentID, SIMSessionGeneration: request.SIMSessionGeneration, Action: request.Action, State: "verified"}, nil
}

func TestSIMPINHandlerForwardsExactModemFence(t *testing.T) {
	stub := &pinRuntimeStub{}
	handler, err := NewSIMPINHandler(stub)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(`{"operation_id":"pin-operation-1","card_id":"89010000000000000001","equipment_id":"862547055201716","action":"verify","pin":"1234"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.request.AttachmentID != "attach-1" || stub.request.SIMSessionGeneration != "session-1" || strings.Contains(response.Body.String(), "1234") {
		t.Fatalf("status=%d body=%s request=%+v", response.Code, response.Body.String(), stub.request)
	}
}

func TestSIMPINHandlerRejectsUnsafeRequestBeforeResolution(t *testing.T) {
	handler, _ := NewSIMPINHandler(&pinRuntimeStub{})
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
	payload := `{"operation_id":"pin-operation-1","card_id":"89010000000000000001","equipment_id":"862547055201716","action":"verify","pin":"1234"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/sim-pin", strings.NewReader(payload))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}
	first := stub.request
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
