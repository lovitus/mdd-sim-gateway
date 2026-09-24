package agentcall

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func TestTerminalReceiptSurvivesRestartWithoutTouchingNewCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord(time.Now().Add(time.Minute))
	if _, _, err = store.Begin(record); err != nil {
		t.Fatal(err)
	}
	if err = store.ConfirmTarget(record.AttachmentID, record.EquipmentID, record.CardID, time.Now(), "confirmed_hangup"); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other := record
	other.LeaseID = "new-lease"
	other.OperationID = "new-operation"
	if _, _, err = store.Begin(other); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewManager(store, operatorFunc(func(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error) {
		t.Fatal("receipt accessed hardware of new call")
		return agentmodem.OperationResult{}, nil
	}))
	query := testOperation(agentmodem.OperationCallReceipt)
	result, err := manager.Operate(t.Context(), query)
	if err != nil || !result.Call.TerminalConfirmed || result.Call.Strategy != "stored_terminal" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	query.OperationID = "different-original-operation"
	if _, err = manager.Operate(t.Context(), query); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatal("mismatched operation accepted", err)
	}
	records, err := store.Records()
	if err != nil || len(records) != 1 || records[0].LeaseID != other.LeaseID {
		t.Fatal("new call changed", err)
	}
}

func TestReceiptConfirmsNaturalIdleWithoutSendingHangup(t *testing.T) {
	store := openTestStore(t)
	record := testRecord(time.Now().Add(time.Minute))
	if _, _, err := store.Begin(record); err != nil {
		t.Fatal(err)
	}
	reads := 0
	manager, _ := NewManager(store, operatorFunc(func(_ context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
		if operation.Action != agentmodem.OperationCallStatus {
			t.Fatalf("receipt sent %s", operation.Action)
		}
		reads++
		return callResult("idle", false), nil
	}))
	result, err := manager.Operate(t.Context(), testOperation(agentmodem.OperationCallReceipt))
	if err != nil || !result.Call.TerminalConfirmed || reads != 2 {
		t.Fatalf("result=%+v reads=%d err=%v", result, reads, err)
	}
	if _, err = manager.Operate(t.Context(), testOperation(agentmodem.OperationCallReceipt)); err != nil || reads != 2 {
		t.Fatal("receipt replay probed modem", err)
	}
}

func TestUnconfirmedAndNeverDispatchedAreNotTerminalReceipts(t *testing.T) {
	store := openTestStore(t)
	record := testRecord(time.Now().Add(time.Minute))
	if _, _, err := store.Begin(record); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewManager(store, operatorFunc(func(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error) {
		return callResult("active", false), nil
	}))
	if _, err := manager.Operate(t.Context(), testOperation(agentmodem.OperationCallReceipt)); !errors.Is(err, ErrTerminalUnconfirmed) {
		t.Fatal(err)
	}
	if err := store.ClearTarget(record.AttachmentID, record.EquipmentID, record.CardID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Operate(t.Context(), testOperation(agentmodem.OperationCallReceipt)); !errors.Is(err, ErrLeaseNotFound) {
		t.Fatal("pre-dispatch clear fabricated a terminal", err)
	}
}

func TestTerminalKeysDoNotCollideAcrossModemsWithSameLeaseID(t *testing.T) {
	store := openTestStore(t)
	a := testRecord(time.Now().Add(time.Minute))
	b := a
	b.EquipmentID = "862547055201717"
	b.AttachmentID = "attachment-2"
	b.OperationID = "operation-2"
	for _, record := range []Record{a, b} {
		if _, _, err := store.Begin(record); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []Record{a, b} {
		if err := store.ConfirmTarget(record.AttachmentID, record.EquipmentID, record.CardID, time.Now(), "confirmed_hangup"); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []Record{a, b} {
		proof, found, err := store.Terminal(record.AttachmentID, record.EquipmentID, record.CardID, record.LeaseID, record.OperationID)
		if err != nil || !found || proof.OperationID != record.OperationID {
			t.Fatal("terminal overwritten", err)
		}
		if _, _, err = store.Begin(record); !errors.Is(err, ErrLeaseConflict) {
			t.Fatal("terminal lease reused", err)
		}
	}
}

func TestReceiptRejectsUnstableOrStaleIdleWithoutClearingLease(t *testing.T) {
	for _, scenario := range []string{"active", "non_authoritative", "stale", "same_timestamp", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			store := openTestStore(t)
			record := testRecord(time.Now().Add(time.Minute))
			if _, _, err := store.Begin(record); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reads := 0
			first := time.Now()
			manager, _ := NewManager(store, operatorFunc(func(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error) {
				reads++
				result := callResult("idle", false)
				if reads == 1 {
					first = result.Call.ObservedAt
					if scenario == "cancelled" {
						cancel()
					}
					return result, nil
				}
				switch scenario {
				case "active":
					result.Call.State = "active"
				case "non_authoritative":
					result.Call.Authoritative = false
				case "stale":
					result.Call.ObservedAt = time.Now().Add(-time.Minute)
				case "same_timestamp":
					result.Call.ObservedAt = first
				}
				return result, nil
			}))
			if _, err := manager.Operate(ctx, testOperation(agentmodem.OperationCallReceipt)); err == nil {
				t.Fatal("unstable idle confirmed")
			}
			rows, err := store.Records()
			if err != nil || len(rows) != 1 {
				t.Fatal("lease cleared", err)
			}
			if _, found, err := store.Terminal(record.AttachmentID, record.EquipmentID, record.CardID, record.LeaseID, record.OperationID); err != nil || found {
				t.Fatal("false terminal stored", err)
			}
		})
	}
}
