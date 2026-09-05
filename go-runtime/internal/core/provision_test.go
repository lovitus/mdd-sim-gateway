package core

import (
	"context"
	"encoding/json"
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
	} else {
		if stub.result.OperationID == "" {
			stub.result.OperationID = request.OperationID
		}
		if stub.result.EquipmentID == "" {
			stub.result.EquipmentID = request.EquipmentID
		}
		if stub.result.CardID == "" {
			stub.result.CardID = request.CardID
		}
		if stub.result.SIMSessionGeneration == "" {
			stub.result.SIMSessionGeneration = request.SIMSessionGeneration
		}
	}
	return stub.result, nil
}

func (stub *provisionRuntimeStub) ReconcileProvision(_ context.Context, _, _ string, request agentlink.ProvisionRequest) (agentlink.ProvisionResponse, error) {
	stub.calls++
	result := stub.result
	if result.State == "" {
		result.State = agentlink.ProvisionApplied
	}
	result.OperationID = request.OperationID
	result.EquipmentID = request.EquipmentID
	result.CardID = request.CardID
	if result.SIMSessionGeneration == "" {
		result.SIMSessionGeneration = request.SIMSessionGeneration
	}
	return result, nil
}

func withProvisionPrecondition(t *testing.T, store *linecatalog.Store, payload string) string {
	t.Helper()
	var command agentlink.ProvisionCommand
	if err := json.Unmarshal([]byte(payload), &command); err != nil {
		t.Fatal(err)
	}
	command.OperationID = "preflight-" + command.OperationID
	readbackPayload, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	readback, err := NewProvisionReadbackHandler(&provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}, store)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	readback.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/readback", strings.NewReader(string(readbackPayload))))
	if response.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", response.Code, response.Body.String())
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatal(err)
	}
	request["preflight_operation_id"] = command.OperationID
	result, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return string(result)
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
	payload := withProvisionPrecondition(t, store, `{"operation_id":"provision-1","line_id":"line-1","line_name":"Test","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000","apn":"internet","egress_country":"US"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusAccepted || stub.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
	line, err := store.Get("line-1")
	if err != nil || line.Enabled || line.Network.ActiveAPN != "provision-apn" || len(line.Network.APNProfiles) != 1 || line.Network.APNProfiles[0].APN != "internet" {
		t.Fatalf("line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("provision-1")
	if err != nil || !found || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestProvisionReconcileAdvancesOnlyMatchingUnknownOperation(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{}
	provision, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"reconcile-1","line_id":"line-1","line_name":"Test","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000","apn":"internet"}`)
	first := httptest.NewRecorder()
	provision.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("provision status=%d body=%s", first.Code, first.Body.String())
	}
	stub.result.State = agentlink.ProvisionApplied
	reconcile, err := NewProvisionReconcileHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	reconcile.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/reconcile", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("reconcile status=%d body=%s", response.Code, response.Body.String())
	}
	receipt, found, err := store.GetOperation("reconcile-1")
	if err != nil || !found || receipt.State != linecatalog.OperationReconciled {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestProvisionReconcileRejectsNonUnknownAndDigestReuse(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}
	provision, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"reconcile-2","line_id":"line-1","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	first := httptest.NewRecorder()
	provision.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	reconcile, err := NewProvisionReconcileHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	reconcile.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/reconcile", strings.NewReader(payload)))
	if response.Code != http.StatusConflict {
		t.Fatalf("non-unknown status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProvisionReconcileReturnsDurableUnknownWithoutPolling(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{}
	provision, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"reconcile-still-unknown","line_id":"line-1","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	first := httptest.NewRecorder()
	provision.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("provision status=%d body=%s", first.Code, first.Body.String())
	}
	reconcile, err := NewProvisionReconcileHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	reconcile.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/reconcile", strings.NewReader(payload)))
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"state":"unknown"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProvisionReadbackRecordsSuccessWithoutChangingCatalog(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}
	handler, err := NewProvisionReadbackHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"readback-provision-1","line_id":"line-1","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/readback", strings.NewReader(payload)))
	if response.Code != http.StatusOK || stub.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
	receipt, found, err := store.GetOperation("readback-provision-1")
	if err != nil || !found || receipt.Kind != linecatalog.OperationProvisionReadback ||
		receipt.State != linecatalog.OperationSucceeded || receipt.OutcomeCode != "provision_readback_verified" {
		t.Fatalf("receipt=%+v found=%t err=%v", receipt, found, err)
	}
	after, err := store.Snapshot()
	if err != nil || after.Revision != before.Revision || len(after.Lines) != 0 {
		t.Fatalf("catalog changed before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestProvisionReadbackReturnsReboundCurrentSession(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{
		State: agentlink.ProvisionApplied, SIMSessionGeneration: "current-session",
	}}
	handler, err := NewProvisionReadbackHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"readback-rebound-session","line_id":"line-1","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"stale-session","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/readback", strings.NewReader(payload)))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale Core target should fail before Agent in this unit boundary: status=%d body=%s", response.Code, response.Body.String())
	}
	stub.result.SIMSessionGeneration = "session-2"

	validPayload := strings.Replace(payload, "stale-session", "session-1", 1)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/readback", strings.NewReader(validPayload)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"sim_session_generation":"session-2"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	receipt, found, err := store.GetOperation("readback-rebound-session")
	if err != nil || !found || receipt.SIMSessionGeneration != "session-2" {
		t.Fatalf("receipt=%+v found=%t err=%v", receipt, found, err)
	}
}

func TestProvisionReadbackRecordsUnknownAndReplaysWithoutAgent(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{
		State: agentlink.ProvisionUnknown, ErrorCode: "provision_target_not_ready",
	}}
	handler, err := NewProvisionReadbackHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"readback-provision-unknown","line_id":"line-1","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	for attempt := 0; attempt < 2; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/readback", strings.NewReader(payload)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("attempt=%d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	if stub.calls != 1 {
		t.Fatalf("Agent calls=%d, want one", stub.calls)
	}
	receipt, found, err := store.GetOperation("readback-provision-unknown")
	if err != nil || !found || receipt.State != linecatalog.OperationUnknown || receipt.ErrorCode != "provision_target_not_ready" {
		t.Fatalf("receipt=%+v found=%t err=%v", receipt, found, err)
	}
}

func TestProvisionRequiresSuccessfulReadbackPrecondition(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"provision-without-preflight","line_id":"line-1","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusPreconditionRequired || stub.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
	if snapshot, snapshotErr := store.Snapshot(); snapshotErr != nil || len(snapshot.Lines) != 0 {
		t.Fatalf("catalog changed snapshot=%+v err=%v", snapshot, snapshotErr)
	}
}

func TestProvisionConsumesExactReadbackPreconditionOnce(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	base := `{"operation_id":"provision-once","line_id":"line-once","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	payload := withProvisionPrecondition(t, store, base)
	var request map[string]any
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatal(err)
	}
	preflightID := request["preflight_operation_id"].(string)
	request["operation_id"] = preflightID
	collisionPayload, _ := json.Marshal(request)
	collision := httptest.NewRecorder()
	handler.ServeHTTP(collision, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(string(collisionPayload))))
	if collision.Code != http.StatusConflict || stub.calls != 0 {
		t.Fatalf("collision status=%d calls=%d body=%s", collision.Code, stub.calls, collision.Body.String())
	}
	request["operation_id"] = "provision-once"
	payloadBytes, _ := json.Marshal(request)
	payload = string(payloadBytes)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if first.Code != http.StatusOK || stub.calls != 1 {
		t.Fatalf("first status=%d calls=%d body=%s", first.Code, stub.calls, first.Body.String())
	}
	preflight, found, err := store.GetOperation(preflightID)
	if err != nil || !found || preflight.State != linecatalog.OperationReconciled ||
		preflight.OutcomeCode != "provision_precondition_consumed" {
		t.Fatalf("preflight=%+v found=%t err=%v", preflight, found, err)
	}
	request["operation_id"] = "provision-twice"
	secondPayload, _ := json.Marshal(request)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(string(secondPayload))))
	if second.Code != http.StatusPreconditionFailed || stub.calls != 1 {
		t.Fatalf("second status=%d calls=%d body=%s", second.Code, stub.calls, second.Body.String())
	}
}

func TestProvisionRejectsExpiredReadbackPrecondition(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"provision-expired","line_id":"line-expired","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	handler.now = func() time.Time { return time.Now().Add(provisionPreconditionTTL + time.Second) }
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusPreconditionFailed || stub.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
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

func TestProvisionHandlerFinalizesRequestedEnabledStateOnlyAfterAgentSuccess(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"provision-success","line_id":"line-success","line_name":"Enabled","enabled":true,"equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	line, err := store.Get("line-success")
	if err != nil || !line.Enabled {
		t.Fatalf("line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("provision-success")
	if err != nil || !found || receipt.State != linecatalog.OperationSucceeded {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestProvisionHandlerDoesNotFinalizeFailedAgentResult(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{
		State: agentlink.ProvisionFailed, ErrorCode: "provision_hardware_failed",
	}}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"provision-failed","line_id":"line-failed","enabled":true,"equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	line, err := store.Get("line-failed")
	if err != nil || line.Enabled {
		t.Fatalf("failed provision activated line: line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("provision-failed")
	if err != nil || !found || receipt.State != linecatalog.OperationFailed ||
		receipt.ErrorCode != "provision_hardware_failed" {
		t.Fatalf("receipt=%+v found=%t err=%v", receipt, found, err)
	}
}

func TestProvisionHandlerDoesNotFinalizePreparedAgentResult(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionPrepared}}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"provision-prepared","line_id":"line-prepared","enabled":true,"equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	line, err := store.Get("line-prepared")
	if err != nil || line.Enabled {
		t.Fatalf("prepared provision activated line: line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("provision-prepared")
	if err != nil || !found || receipt.State != linecatalog.OperationUnknown ||
		receipt.ErrorCode != "provision_unrecognized_state" {
		t.Fatalf("receipt=%+v found=%t err=%v", receipt, found, err)
	}
}

func TestProvisionHandlerRejectsMismatchedAgentIdentityAsUnknown(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{OperationID: "wrong-operation", State: agentlink.ProvisionApplied}}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"provision-fence","line_id":"line-fence","enabled":true,"equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	line, err := store.Get("line-fence")
	if err != nil || line.Enabled {
		t.Fatalf("line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("provision-fence")
	if err != nil || !found || receipt.State != linecatalog.OperationUnknown || receipt.ErrorCode != "provision_response_identity_mismatch" {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
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
	payload := withProvisionPrecondition(t, store, `{"operation_id":"provision-1","line_id":"line-1","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
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
		CardID: "89010000000000000001", SIM: linecatalog.SIMConfig{IMSI: "460001234567890", MCC: "460", MNC: "01", SMSC: "+8613800138000"},
		Network: linecatalog.NetworkConfig{APNProfiles: []linecatalog.APNProfile{{ID: "carrier-profile", Name: "Carrier", APN: "internet", Auth: "NONE"}}, ActiveAPN: "carrier-profile"}}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewReprovisionHandler(&provisionRuntimeStub{}, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"reprovision-1","line_id":"line-1","line_name":"new","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460009999999999","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000","apn":"carrier-profile"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/reprovision", strings.NewReader(payload)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	line, err := store.Get("line-1")
	if err != nil || line.Name != "new" || line.SIM.IMSI != "460009999999999" || line.Network.APNProfiles[0].APN != "internet" {
		t.Fatalf("line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("reprovision-1")
	if err != nil || !found || receipt.Kind != linecatalog.OperationReprovision || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestReprovisionSuccessPreservesExistingEnabledIntent(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1", Name: "old", Enabled: true,
		CardID: "89010000000000000001", SIM: linecatalog.SIMConfig{IMSI: "460001234567890", MCC: "460", MNC: "01", SMSC: "+8613800138000"}}); err != nil {
		t.Fatal(err)
	}
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}
	handler, err := NewReprovisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"reprovision-success","line_id":"line-1","line_name":"new","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/reprovision", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	line, err := store.Get("line-1")
	if err != nil || !line.Enabled {
		t.Fatalf("line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("reprovision-success")
	if err != nil || !found || receipt.State != linecatalog.OperationSucceeded {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}
