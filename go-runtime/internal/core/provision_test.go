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

type provisionRuntimeStub struct {
	result agentlink.ProvisionResponse
	calls  int
}

func (stub *provisionRuntimeStub) ResolveModemTargetForAction(_, _ string, _ agentlink.ModemAction) (agentlink.ModemTarget, error) {
	return agentlink.ModemTarget{
		AgentID: "agent-1", ProcessGeneration: "process-1", AttachmentID: "attach-1",
		EquipmentID: "862547055201716", CardID: "89010000000000000001",
		SIMSessionGeneration: "session-1",
	}, nil
}

func (stub *provisionRuntimeStub) ExecuteProvision(_ context.Context, _, _ string, request agentlink.ProvisionRequest) (agentlink.ProvisionResponse, error) {
	stub.calls++
	if stub.result.State == "" {
		stub.result = agentlink.ProvisionResponse{
			OperationID: request.OperationID, EquipmentID: request.EquipmentID,
			CardID: request.CardID, SIMSessionGeneration: request.SIMSessionGeneration,
			State: agentlink.ProvisionUnknown, ErrorCode: "executor_unavailable",
		}
	}
	return stub.result, nil
}

func TestProvisionHandlerCreatesDisabledLineAndRecordsUnknown(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"provision-1","line_id":"line-1","line_name":"Test","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000","egress_country":"US"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusAccepted || stub.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
	line, err := store.Get("line-1")
	if err != nil || line.Enabled {
		t.Fatalf("line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("provision-1")
	if err != nil || !found || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestProvisionHandlerRejectsMismatchedAttachment(t *testing.T) {
	stub := &provisionRuntimeStub{}
	handler, _ := NewProvisionHandler(stub, nil)
	payload := `{"operation_id":"provision-1","line_id":"line-1","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"wrong","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusConflict || stub.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
}

func TestProvisionHandlerReplaysUnknownWithoutCallingAgent(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{}
	handler, _ := NewProvisionHandler(stub, store)
	payload := `{"operation_id":"provision-1","line_id":"line-1","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	for attempt := 0; attempt < 2; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("attempt=%d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	if stub.calls != 1 {
		t.Fatalf("agent calls=%d, want 1", stub.calls)
	}
}

func TestReprovisionHandlerAtomicallyReplacesExistingLine(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1", Name: "old",
		CardID: "89010000000000000001", SIM: linecatalog.SIMConfig{IMSI: "460001234567890", MCC: "460", MNC: "01", SMSC: "+8613800138000"}}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewReprovisionHandler(&provisionRuntimeStub{}, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"reprovision-1","line_id":"line-1","line_name":"new","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460009999999999","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/reprovision", strings.NewReader(payload)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	line, err := store.Get("line-1")
	if err != nil || line.Name != "new" || line.SIM.IMSI != "460009999999999" {
		t.Fatalf("line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("reprovision-1")
	if err != nil || !found || receipt.Kind != linecatalog.OperationReprovision || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}
