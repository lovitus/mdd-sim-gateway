package agentcall

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type operatorFunc func(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error)

func (function operatorFunc) Operate(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	return function(ctx, operation)
}

func TestManagerCommitsLeaseBeforeDialAndMakesRetryIdempotent(t *testing.T) {
	store := openTestStore(t)
	dials := 0
	operator := operatorFunc(func(_ context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
		if operation.Action == agentmodem.OperationCallStatus {
			state := "idle"
			if dials > 0 {
				state = "dialing"
			}
			return callResult(state, false), nil
		}
		dials++
		records, err := store.Records()
		if err != nil || len(records) != 1 || records[0].LeaseID != operation.LeaseID {
			t.Fatalf("lease was not durable before dial: records=%+v err=%v", records, err)
		}
		return callResult("dialing", false), nil
	})
	manager, _ := NewManager(store, operator)
	operation := testOperation(agentmodem.OperationCallDial)
	operation.Number = "+448001076285"
	first, err := manager.Operate(context.Background(), operation)
	if err != nil || first.LeaseID != operation.LeaseID || dials != 1 {
		t.Fatalf("first=%+v dials=%d err=%v", first, dials, err)
	}
	second, err := manager.Operate(context.Background(), operation)
	if err != nil || second.Call.State != "dialing" || dials != 1 {
		t.Fatalf("retry=%+v dials=%d err=%v", second, dials, err)
	}
}

func TestAmbiguousDialSurvivesRestartAndExpiredLeasePhysicallyHangsUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "paid-calls.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	manager, _ := NewManager(store, operatorFunc(func(_ context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
		if operation.Action == agentmodem.OperationCallStatus {
			return callResult("idle", false), nil
		}
		return agentmodem.OperationResult{}, errors.New("AT response was lost")
	}))
	now := time.Unix(1700000000, 0)
	manager.now = func() time.Time { return now }
	operation := testOperation(agentmodem.OperationCallDial)
	operation.Number = "+448001076285"
	if _, err := manager.Operate(context.Background(), operation); err == nil {
		t.Fatal("ambiguous dial unexpectedly succeeded")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	hangups := 0
	restarted, _ := NewManager(reopened, operatorFunc(func(_ context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
		if operation.Action != agentmodem.OperationCallHangup {
			t.Fatalf("restart operation=%s", operation.Action)
		}
		hangups++
		return callResult("idle", true), nil
	}))
	restarted.now = func() time.Time { return now.Add(time.Minute) }
	if err := restarted.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err := reopened.Records()
	if err != nil || len(records) != 0 || hangups != 1 {
		t.Fatalf("records=%+v hangups=%d err=%v", records, hangups, err)
	}
}

func TestWatchdogStopsAfterThreeUnconfirmedHangupsAndRetainsLease(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(1700000000, 0)
	if _, _, err := store.Begin(testRecord(now.Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	manager, _ := NewManager(store, operatorFunc(func(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error) {
		attempts++
		return agentmodem.OperationResult{}, errors.New("hangup not confirmed")
	}))
	manager.now = func() time.Time { return now }
	for index := 0; index < 5; index++ {
		if err := manager.sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	records, err := store.Records()
	if err != nil || attempts != 3 || len(records) != 1 {
		t.Fatalf("attempts=%d records=%+v err=%v", attempts, records, err)
	}
}

func TestLeaseRenewalRequiresExactAttachmentCardAndLease(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(1700000000, 0)
	if _, _, err := store.Begin(testRecord(now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewManager(store, operatorFunc(func(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error) {
		t.Fatal("renewal touched the modem")
		return agentmodem.OperationResult{}, nil
	}))
	manager.now = func() time.Time { return now }
	operation := testOperation(agentmodem.OperationCallRenew)
	result, err := manager.Operate(context.Background(), operation)
	if err != nil || !result.LeaseUntil.Equal(now.Add(leaseDuration)) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	operation.AttachmentID = "replacement"
	if _, err := manager.Operate(context.Background(), operation); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("replacement renewal error=%v", err)
	}
}

func TestHangupCannotClearLeaseBetweenDurableArmAndDial(t *testing.T) {
	store := openTestStore(t)
	dialEntered := make(chan struct{})
	releaseDial := make(chan struct{})
	hangupEntered := make(chan struct{})
	manager, _ := NewManager(store, operatorFunc(func(_ context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
		switch operation.Action {
		case agentmodem.OperationCallStatus:
			return callResult("idle", false), nil
		case agentmodem.OperationCallDial:
			close(dialEntered)
			<-releaseDial
			return callResult("dialing", false), nil
		case agentmodem.OperationCallHangup:
			close(hangupEntered)
			return callResult("idle", true), nil
		default:
			return agentmodem.OperationResult{}, errors.New("unexpected operation")
		}
	}))
	dialOperation := testOperation(agentmodem.OperationCallDial)
	dialOperation.Number = "+448001076285"
	dialDone := make(chan error, 1)
	go func() { _, err := manager.Operate(context.Background(), dialOperation); dialDone <- err }()
	<-dialEntered
	hangupDone := make(chan error, 1)
	hangupStarted := make(chan struct{})
	go func() {
		close(hangupStarted)
		hangup := testOperation(agentmodem.OperationCallHangup)
		hangup.LeaseID = ""
		_, err := manager.Operate(context.Background(), hangup)
		hangupDone <- err
	}()
	<-hangupStarted
	select {
	case <-hangupEntered:
		t.Fatal("hangup crossed the durable-arm/dial critical section")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseDial)
	if err := <-dialDone; err != nil {
		t.Fatal(err)
	}
	if err := <-hangupDone; err != nil {
		t.Fatal(err)
	}
	records, err := store.Records()
	if err != nil || len(records) != 0 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "state", "paid-calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testOperation(action agentmodem.OperationAction) agentmodem.Operation {
	return agentmodem.Operation{
		OperationID: "operation-1", AttachmentID: "attachment-1", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: action, LeaseID: "lease-1",
	}
}

func testRecord(expiresAt time.Time) Record {
	operation := testOperation(agentmodem.OperationCallDial)
	return Record{
		SchemaVersion: schemaVersion, LeaseID: operation.LeaseID, OperationID: operation.OperationID,
		AttachmentID: operation.AttachmentID, EquipmentID: operation.EquipmentID, CardID: operation.CardID,
		Direction: "out", ExpiresAt: expiresAt,
	}
}

func callResult(state string, terminal bool) agentmodem.OperationResult {
	return agentmodem.OperationResult{Call: agentmodem.CallResult{
		State: state, ObservedAt: time.Now(), Authoritative: true, TerminalConfirmed: terminal,
		Strategy: func() string {
			if terminal {
				return "already_idle"
			}
			return ""
		}(),
	}}
}
