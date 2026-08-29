package agentsms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

var ErrSubmitUncertain = errors.New("SMS submission result is uncertain")

type possiblySent interface{ PossiblySentSMS() bool }

type Manager struct {
	store *Store
	calls agentmodem.ManagedOperator
	now   func() time.Time
}

func NewManager(store *Store, calls agentmodem.ManagedOperator) (*Manager, error) {
	if store == nil || calls == nil {
		return nil, errors.New("invalid SMS manager configuration")
	}
	return &Manager{store: store, calls: calls, now: time.Now}, nil
}

func (manager *Manager) Operate(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	if operation.Action == agentmodem.OperationSMSList {
		return manager.calls.Operate(ctx, operation)
	}
	if operation.Action != agentmodem.OperationSMSSend {
		return manager.calls.Operate(ctx, operation)
	}
	digest := sha256.Sum256([]byte(operation.Body))
	record, created, err := manager.store.Begin(Record{
		OperationID: operation.OperationID, AttachmentID: operation.AttachmentID,
		EquipmentID: operation.EquipmentID, CardID: operation.CardID, Recipient: operation.Number,
		BodySHA256: hex.EncodeToString(digest[:]), CreatedAt: manager.now().UTC(),
	})
	if err != nil {
		return agentmodem.OperationResult{}, err
	}
	if !created {
		if record.State == "submitted" {
			return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "submitted", References: record.References}}, nil
		}
		return agentmodem.OperationResult{}, ErrSubmitUncertain
	}
	result, operationErr := manager.calls.Operate(ctx, operation)
	if operationErr != nil {
		var uncertain possiblySent
		if errors.As(operationErr, &uncertain) && uncertain.PossiblySentSMS() {
			_, _ = manager.store.Mark(operation.OperationID, "uncertain", result.SMS.References)
			return agentmodem.OperationResult{}, ErrSubmitUncertain
		}
		_ = manager.store.Delete(operation.OperationID)
		return agentmodem.OperationResult{}, operationErr
	}
	record, err = manager.store.Mark(operation.OperationID, "submitted", result.SMS.References)
	if err != nil {
		return agentmodem.OperationResult{}, ErrSubmitUncertain
	}
	result.SMS.References = append([]int(nil), record.References...)
	return result, nil
}

func (manager *Manager) Run(ctx context.Context) error { return manager.calls.Run(ctx) }

func (manager *Manager) Close() error {
	var callErr error
	if closer, ok := manager.calls.(interface{ Close() error }); ok {
		callErr = closer.Close()
	}
	return errors.Join(manager.store.Close(), callErr)
}
