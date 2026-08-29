package agentsms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type managedOperator struct {
	operate func(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error)
}

func (operator *managedOperator) Operate(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	return operator.operate(ctx, operation)
}

func (*managedOperator) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

type uncertainError struct{}

func (uncertainError) Error() string         { return "response lost after submit" }
func (uncertainError) PossiblySentSMS() bool { return true }

func TestManagerPersistsBeforeSubmitAndDoesNotResendSuccessfulRetry(t *testing.T) {
	store := openStore(t)
	submits := 0
	operator := &managedOperator{operate: func(_ context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
		submits++
		record, created, err := store.Begin(recordFor(operation))
		if err != nil || created || record.State != "prepared" {
			t.Fatalf("submission was not durable first: record=%+v created=%v err=%v", record, created, err)
		}
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "submitted", References: []int{0, 17}}}, nil
	}}
	calls := &managedOperator{operate: operator.operate}
	manager, err := NewManager(store, calls)
	if err != nil {
		t.Fatal(err)
	}
	operation := smsOperation()
	first, err := manager.Operate(context.Background(), operation)
	if err != nil || submits != 1 || len(first.SMS.References) != 2 {
		t.Fatalf("first=%+v submits=%d err=%v", first, submits, err)
	}
	second, err := manager.Operate(context.Background(), operation)
	if err != nil || submits != 1 || len(second.SMS.References) != 2 || second.SMS.References[0] != 0 {
		t.Fatalf("retry=%+v submits=%d err=%v", second, submits, err)
	}
	operation.Body = "different body"
	if _, err := manager.Operate(context.Background(), operation); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting retry err=%v", err)
	}
}

func TestManagerRetainsUncertainSubmissionAndNeverResendsIt(t *testing.T) {
	store := openStore(t)
	submits := 0
	operator := &managedOperator{operate: func(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error) {
		submits++
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{References: []int{9}}}, uncertainError{}
	}}
	manager, _ := NewManager(store, &managedOperator{operate: operator.operate})
	operation := smsOperation()
	if _, err := manager.Operate(context.Background(), operation); !errors.Is(err, ErrSubmitUncertain) {
		t.Fatalf("first uncertain err=%v", err)
	}
	if _, err := manager.Operate(context.Background(), operation); !errors.Is(err, ErrSubmitUncertain) || submits != 1 {
		t.Fatalf("retry err=%v submits=%d", err, submits)
	}
	record, created, err := store.Begin(recordFor(operation))
	if err != nil || created || record.State != "uncertain" || len(record.References) != 1 || record.References[0] != 9 {
		t.Fatalf("record=%+v created=%v err=%v", record, created, err)
	}
}

func TestManagerDeletesDefiniteFailureSoExplicitRetryCanSubmit(t *testing.T) {
	store := openStore(t)
	submits := 0
	operator := &managedOperator{operate: func(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error) {
		submits++
		if submits == 1 {
			return agentmodem.OperationResult{}, errors.New("modem rejected before prompt")
		}
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "submitted", References: []int{3}}}, nil
	}}
	manager, _ := NewManager(store, &managedOperator{operate: operator.operate})
	operation := smsOperation()
	if _, err := manager.Operate(context.Background(), operation); err == nil {
		t.Fatal("definite failure unexpectedly succeeded")
	}
	if result, err := manager.Operate(context.Background(), operation); err != nil || submits != 2 || result.SMS.References[0] != 3 {
		t.Fatalf("retry=%+v submits=%d err=%v", result, submits, err)
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "state", "sms.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func smsOperation() agentmodem.Operation {
	return agentmodem.Operation{
		OperationID: "sms-operation-1", AttachmentID: "attachment-1", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: agentmodem.OperationSMSSend,
		Number: "+85222333322", Body: "hello 世界",
	}
}

func recordFor(operation agentmodem.Operation) Record {
	return Record{
		OperationID: operation.OperationID, AttachmentID: operation.AttachmentID,
		EquipmentID: operation.EquipmentID, CardID: operation.CardID, Recipient: operation.Number,
		BodySHA256: bodyDigest(operation.Body), CreatedAt: time.Now().UTC(),
	}
}

func bodyDigest(body string) string {
	// Kept local to the test so the durable identity is checked against the
	// exact production SHA-256 representation.
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:])
}
