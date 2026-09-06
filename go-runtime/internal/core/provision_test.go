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
	result     agentlink.ProvisionResponse
	executeErr error
	calls      int
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
	if stub.executeErr != nil {
		return agentlink.ProvisionResponse{}, stub.executeErr
	}
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
	payload := withProvisionPrecondition(t, store, `{"operation_id":"reconcile-1","line_id":"line-1","line_name":"Test","enabled":true,"equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000","apn":"internet"}`)
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
	line, err := store.Get("line-1")
	if err != nil || !line.Enabled || line.HardwareProvisionState != "provisioned" {
		t.Fatalf("line=%+v err=%v", line, err)
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

func TestProvisionUsesProofSessionAheadOfHealthProjection(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	readbackRuntime := &provisionRuntimeStub{result: agentlink.ProvisionResponse{
		State: agentlink.ProvisionApplied, SIMSessionGeneration: "session-2",
	}}
	readback, err := NewProvisionReadbackHandler(readbackRuntime, store)
	if err != nil {
		t.Fatal(err)
	}
	readbackPayload := `{"operation_id":"preflight-health-lag","line_id":"line-lag","enabled":true,"equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	response := httptest.NewRecorder()
	readback.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/readback", strings.NewReader(readbackPayload)))
	if response.Code != http.StatusOK {
		t.Fatalf("readback status=%d body=%s", response.Code, response.Body.String())
	}
	provisionRuntime := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}
	provision, err := NewProvisionHandler(provisionRuntime, store)
	if err != nil {
		t.Fatal(err)
	}
	provisionPayload := `{"operation_id":"provision-health-lag","preflight_operation_id":"preflight-health-lag","line_id":"line-lag","enabled":true,"equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-2","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	response = httptest.NewRecorder()
	provision.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(provisionPayload)))
	if response.Code != http.StatusOK || provisionRuntime.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, provisionRuntime.calls, response.Body.String())
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
	reprovision, err := NewReprovisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	reprovision.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/reprovision", strings.NewReader(string(secondPayload))))
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
	payload := withProvisionPrecondition(t, store, `{"operation_id":"provision-success","line_id":"line-success","line_name":"Enabled","enabled":true,"equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","imeisv":"3567890123456789","smsc":"+8613800138000","ims_apn":"ims","idr_mode":"fqdn","cp_mode":"dual"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	line, err := store.Get("line-success")
	if err != nil || !line.Enabled || line.SIM.IMEISV != "3567890123456789" || line.Network.IMSAPN != "ims" ||
		line.Network.IDRMode != "fqdn" || line.Network.CPMode != "dual" {
		t.Fatalf("line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("provision-success")
	if err != nil || !found || receipt.State != linecatalog.OperationSucceeded {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestFirstProvisionPromotesExistingDisabledDraftWithoutLosingDesiredState(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-draft", Name: "draft",
		CardID: "89010000000000000001", HardwareProvisionState: "draft",
		SIM:     linecatalog.SIMConfig{IMSI: "460001234567890", MCC: "460", MNC: "01", IMEI: "356789012345678", SMSC: "+8613800138000"},
		Network: linecatalog.NetworkConfig{EPDGAddress: "epdg.example", PCSCF: []string{"pcscf.example"}, EgressCountry: "cn"},
		IMS:     linecatalog.IMSConfig{IMPI: "subscriber@example", IMPU: "sip:subscriber@example", Domain: "example"}}
	if _, err := store.Put(draft); err != nil {
		t.Fatal(err)
	}
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"first-provision-draft","line_id":"line-draft","line_name":"draft","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000","egress_country":"cn"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	line, lineErr := store.Get("line-draft")
	receipt, found, receiptErr := store.GetOperation("first-provision-draft")
	if response.Code != http.StatusOK || lineErr != nil || line.Enabled ||
		line.HardwareProvisionState != "provisioned" || line.Network.EPDGAddress != "epdg.example" ||
		len(line.Network.PCSCF) != 1 || line.IMS.IMPI != "subscriber@example" ||
		receiptErr != nil || !found || !receipt.ExistingLine || receipt.State != linecatalog.OperationSucceeded {
		t.Fatalf("status=%d line=%+v receipt=%+v lineErr=%v receiptErr=%v body=%s",
			response.Code, line, receipt, lineErr, receiptErr, response.Body.String())
	}
}

func TestFirstProvisionRejectsNonDraftExistingLine(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-existing",
		CardID: "89010000000000000001", SIM: linecatalog.SIMConfig{IMSI: "460001234567890", MCC: "460", MNC: "01"}}); err != nil {
		t.Fatal(err)
	}
	stub := &provisionRuntimeStub{}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"first-provision-existing","line_id":"line-existing","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	if response.Code != http.StatusConflict || stub.calls != 0 ||
		!strings.Contains(response.Body.String(), "provision_requires_disabled_draft") {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
}

func TestFirstProvisionUnknownReconcilePromotesDraftAtomically(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-draft", Name: "draft",
		CardID: "89010000000000000001", HardwareProvisionState: "draft",
		SIM: linecatalog.SIMConfig{IMSI: "460001234567890", MCC: "460", MNC: "01", IMEI: "356789012345678", SMSC: "+8613800138000"}}
	if _, err := store.Put(draft); err != nil {
		t.Fatal(err)
	}
	stub := &provisionRuntimeStub{}
	handler, err := NewProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"first-provision-reconcile","line_id":"line-draft","line_name":"draft","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460001234567890","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
	line, _ := store.Get("line-draft")
	if response.Code != http.StatusAccepted || line.HardwareProvisionState != "draft" || line.Enabled {
		t.Fatalf("status=%d line=%+v body=%s", response.Code, line, response.Body.String())
	}
	stub.result = agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}
	reconcile, err := NewProvisionReconcileHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	reconcile.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/reconcile", strings.NewReader(payload)))
	line, _ = store.Get("line-draft")
	receipt, found, receiptErr := store.GetOperation("first-provision-reconcile")
	if response.Code != http.StatusOK || line.HardwareProvisionState != "provisioned" || line.Enabled ||
		receiptErr != nil || !found || receipt.State != linecatalog.OperationReconciled {
		t.Fatalf("status=%d line=%+v receipt=%+v err=%v body=%s",
			response.Code, line, receipt, receiptErr, response.Body.String())
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

func TestReprovisionStagesOldDesiredStateAndPublishesCandidateOnReconcile(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1", Name: "old", Enabled: true,
		CardID: "89010000000000000001", SIM: linecatalog.SIMConfig{IMSI: "460001234567890", MCC: "460", MNC: "01", SMSC: "+8613800138000"},
		Network: linecatalog.NetworkConfig{EPDGAddress: "epdg.example", PCSCF: []string{"pcscf.example"}, APNProfiles: []linecatalog.APNProfile{{ID: "carrier-profile", Name: "Carrier", APN: "internet", Auth: "PAP", Username: "user", Password: "secret", PasswordSet: true}}, ActiveAPN: "carrier-profile"},
		IMS: linecatalog.IMSConfig{IMPI: "subscriber@example", IMPU: "sip:subscriber@example",
			Domain: "example", UserAgent: "MDD-test"}}); err != nil {
		t.Fatal(err)
	}
	stub := &provisionRuntimeStub{}
	handler, err := NewReprovisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"reprovision-1","line_id":"line-1","line_name":"new","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460009999999999","mcc":"460","mnc":"01","imei":"356789012345678","imeisv":"3567890123456789","smsc":"+8613800138000","apn":"carrier-profile","ims_apn":"ims-custom","idr_mode":"fqdn","cp_mode":"v4"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/reprovision", strings.NewReader(payload)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	line, err := store.Get("line-1")
	if err != nil || line.Enabled || line.Name != "old" || line.SIM.IMSI != "460001234567890" ||
		line.Network.EPDGAddress != "epdg.example" || len(line.Network.PCSCF) != 1 ||
		line.Network.ActiveAPN != "carrier-profile" || len(line.Network.APNProfiles) != 1 ||
		line.Network.APNProfiles[0].APN != "internet" || line.Network.APNProfiles[0].Auth != "PAP" ||
		line.Network.APNProfiles[0].Username != "user" || line.Network.APNProfiles[0].Password != "secret" ||
		line.IMS.IMPI != "subscriber@example" || line.IMS.IMPU != "sip:subscriber@example" ||
		line.IMS.Domain != "example" || line.IMS.UserAgent != "MDD-test" {
		t.Fatalf("line=%+v err=%v", line, err)
	}
	receipt, found, err := store.GetOperation("reprovision-1")
	if err != nil || !found || receipt.Kind != linecatalog.OperationReprovision || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
	stub.result = agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}
	reconcile, err := NewProvisionReconcileHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	reconcile.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision/reconcile", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("reconcile status=%d body=%s", response.Code, response.Body.String())
	}
	line, err = store.Get("line-1")
	if err != nil || !line.Enabled || line.Name != "new" || line.SIM.IMSI != "460009999999999" ||
		line.SIM.IMEISV != "3567890123456789" || line.Network.IMSAPN != "ims-custom" ||
		line.Network.IDRMode != "fqdn" || line.Network.CPMode != "v4" ||
		line.Network.EPDGAddress != "epdg.example" || line.Network.ActiveAPN != "carrier-profile" ||
		line.Network.APNProfiles[0].Password != "secret" || line.IMS.IMPI != "subscriber@example" {
		t.Fatalf("reconciled line=%+v err=%v", line, err)
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

func TestReprovisionDefinitiveFailureRestoresOldLine(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1", Name: "old", Enabled: true,
		CardID: "89010000000000000001", SIM: linecatalog.SIMConfig{IMSI: "460001234567890", MCC: "460", MNC: "01", SMSC: "+8613800138000"}}
	if _, err := store.Put(original); err != nil {
		t.Fatal(err)
	}
	stub := &provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionFailed, ErrorCode: "provision_active_call"}}
	handler, err := NewReprovisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"reprovision-failed-restore","line_id":"line-1","line_name":"new","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460009999999999","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/reprovision", strings.NewReader(payload)))
	line, lineErr := store.Get("line-1")
	receipt, found, receiptErr := store.GetOperation("reprovision-failed-restore")
	if response.Code != http.StatusBadGateway || lineErr != nil || !line.Enabled || line.Name != original.Name ||
		line.SIM.IMSI != original.SIM.IMSI || receiptErr != nil || !found ||
		receipt.State != linecatalog.OperationFailed || receipt.ErrorCode != "provision_active_call" {
		t.Fatalf("status=%d line=%+v receipt=%+v lineErr=%v receiptErr=%v body=%s",
			response.Code, line, receipt, lineErr, receiptErr, response.Body.String())
	}
}

func TestReprovisionTransportFailureRemainsDisabledAndUnknown(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1", Name: "old", Enabled: true,
		CardID: "89010000000000000001", SIM: linecatalog.SIMConfig{IMSI: "460001234567890", MCC: "460", MNC: "01", SMSC: "+8613800138000"}}
	if _, err := store.Put(original); err != nil {
		t.Fatal(err)
	}
	stub := &provisionRuntimeStub{executeErr: context.DeadlineExceeded}
	handler, err := NewReprovisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := withProvisionPrecondition(t, store, `{"operation_id":"reprovision-transport-unknown","line_id":"line-1","line_name":"new","equipment_id":"862547055201716","card_id":"89010000000000000001","attachment_id":"attach-1","sim_session_generation":"session-1","imsi":"460009999999999","mcc":"460","mnc":"01","imei":"356789012345678","smsc":"+8613800138000"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/reprovision", strings.NewReader(payload)))
	line, lineErr := store.Get("line-1")
	receipt, found, receiptErr := store.GetOperation("reprovision-transport-unknown")
	if response.Code != http.StatusAccepted || lineErr != nil || line.Enabled || line.Name != original.Name ||
		receiptErr != nil || !found || receipt.State != linecatalog.OperationUnknown ||
		receipt.ErrorCode != "provision_unconfirmed" {
		t.Fatalf("status=%d line=%+v receipt=%+v lineErr=%v receiptErr=%v body=%s",
			response.Code, line, receipt, lineErr, receiptErr, response.Body.String())
	}
}
