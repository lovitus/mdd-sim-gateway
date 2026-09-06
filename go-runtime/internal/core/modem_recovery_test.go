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

type modemRecoveryStub struct {
	calls   int
	command agentlink.ModemRecoveryCommand
	err     error
}

func (stub *modemRecoveryStub) ExecuteModemRecoveryCommand(_ context.Context,
	command agentlink.ModemRecoveryCommand,
) (agentlink.ModemRecoveryResponse, error) {
	stub.calls++
	stub.command = command
	if stub.err != nil {
		return agentlink.ModemRecoveryResponse{}, stub.err
	}
	return agentlink.ModemRecoveryResponse{OperationID: command.OperationID, EquipmentID: command.EquipmentID,
		CardID: command.CardID, AttachmentID: "attachment-1", SIMSessionGeneration: "session-1",
		Action: command.Action, State: "accepted"}, nil
}

func TestModemRecoveryHandlerRecordsUnavailableBeforeAgentAsFailed(t *testing.T) {
	store, _ := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	defer store.Close()
	_, _ = store.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Enabled: true,
		CardID: "8985200000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}})
	stub := &modemRecoveryStub{err: agentlink.ErrModemOffline}
	handler, _ := NewModemRecoveryHandler(stub, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/lines/line-1/cellular/soft-restart",
		strings.NewReader(`{"operation_id":"restart-offline","expected_card_id":"8985200000000000001","equipment_id":"862547055201716"}`))
	request.SetPathValue("lineID", "line-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	receipt, found, err := store.GetOperation("restart-offline")
	if response.Code != http.StatusPreconditionFailed || err != nil || !found || receipt.State != linecatalog.OperationFailed ||
		receipt.ErrorCode != "modem_recovery_target_unavailable" {
		t.Fatalf("status=%d receipt=%+v found=%t err=%v", response.Code, receipt, found, err)
	}
}

func TestModemRecoveryHandlerIsExactDurableAndIdempotent(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _ = store.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Enabled: true,
		CardID: "8985200000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}})
	stub := &modemRecoveryStub{}
	handler, _ := NewModemRecoveryHandler(stub, store)
	payload := `{"operation_id":"restart-1","expected_card_id":"8985200000000000001","equipment_id":"862547055201716"}`
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/lines/line-1/cellular/soft-restart", strings.NewReader(payload))
		request.SetPathValue("lineID", "line-1")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("attempt=%d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	if stub.calls != 1 || stub.command.Action != agentlink.ModemSoftRestart {
		t.Fatalf("calls=%d command=%+v", stub.calls, stub.command)
	}
	receipt, found, err := store.GetOperation("restart-1")
	if err != nil || !found || receipt.State != linecatalog.OperationSucceeded ||
		receipt.Kind != linecatalog.OperationModemRecovery || receipt.SIMSessionGeneration != "session-1" {
		t.Fatalf("receipt=%+v found=%t err=%v", receipt, found, err)
	}
}

func TestModemRecoveryHandlerRecordsTransportUnknownWithoutRetry(t *testing.T) {
	store, _ := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	defer store.Close()
	_, _ = store.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Enabled: true,
		CardID: "8985200000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}})
	stub := &modemRecoveryStub{err: context.DeadlineExceeded}
	handler, _ := NewModemRecoveryHandler(stub, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/lines/line-1/cellular/soft-restart",
		strings.NewReader(`{"operation_id":"restart-unknown","expected_card_id":"8985200000000000001","equipment_id":"862547055201716"}`))
	request.SetPathValue("lineID", "line-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	receipt, found, err := store.GetOperation("restart-unknown")
	if response.Code != http.StatusAccepted || stub.calls != 1 || err != nil || !found || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("status=%d calls=%d receipt=%+v found=%t err=%v", response.Code, stub.calls, receipt, found, err)
	}
}
