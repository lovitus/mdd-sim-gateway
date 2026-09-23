package agenthost

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
)

type noReceiptHardware struct{ t *testing.T }

func (o noReceiptHardware) Operate(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error) {
	o.t.Fatal("stored receipt touched hardware")
	return agentmodem.OperationResult{}, nil
}

func TestActualWorkerCallReceiptSurvivesWireValidation(t *testing.T) {
	store, err := agentcall.Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := agentcall.Record{SchemaVersion: 1, LeaseID: "lease-1", OperationID: "original-start", AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001", Direction: "out", ExpiresAt: time.Now().Add(time.Minute)}
	if _, _, err = store.Begin(record); err != nil {
		t.Fatal(err)
	}
	if err = store.ConfirmTarget(record.AttachmentID, record.EquipmentID, record.CardID, time.Now(), "confirmed_hangup"); err != nil {
		t.Fatal(err)
	}
	manager, err := agentcall.NewManager(store, noReceiptHardware{t})
	if err != nil {
		t.Fatal(err)
	}
	worker := &Worker{manager: &agentsim.Manager{}, config: Config{Operations: manager}}
	request := agentlink.ModemRequest{OperationID: record.OperationID, AttachmentID: record.AttachmentID, EquipmentID: record.EquipmentID, CardID: record.CardID, LeaseID: record.LeaseID, Action: agentlink.ModemCallReceipt}
	if err = request.Validate(); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(worker.ExecuteModem(t.Context(), request))
	if err != nil {
		t.Fatal(err)
	}
	var result agentlink.ModemResponse
	if err = json.Unmarshal(wire, &result); err != nil {
		t.Fatal(err)
	}
	if err = result.ValidateFor(request); err != nil || result.Lease != nil || result.Call == nil || !result.Call.TerminalConfirmed {
		t.Fatalf("response=%+v err=%v", result, err)
	}
	status := request
	status.Action = agentlink.ModemCallStatus
	status.LeaseID = ""
	if err = result.ValidateFor(status); err == nil {
		t.Fatal("terminal flag admitted as status")
	}
	request.Number = "+15550100123"
	if request.Validate() == nil {
		t.Fatal("receipt accepted paid command fields")
	}
}
