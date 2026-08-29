package agentcall

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

const (
	armingDuration  = 30 * time.Second
	leaseDuration   = 10 * time.Second
	watchdogEvery   = 500 * time.Millisecond
	maximumAttempts = 3
)

// Manager is the only paid-call operator exposed by the Agent. The durable
// record is committed before ATD/ATA and is removed only after fresh CLCC
// samples confirm that the physical modem is idle.
type Manager struct {
	store    *Store
	operator agentmodem.Operator
	now      func() time.Time

	operationMu sync.Mutex
	mu          sync.Mutex
	attempts    map[string]int
}

func NewManager(store *Store, operator agentmodem.Operator) (*Manager, error) {
	if store == nil || operator == nil {
		return nil, errors.New("invalid paid-call manager configuration")
	}
	return &Manager{store: store, operator: operator, now: time.Now, attempts: map[string]int{}}, nil
}

func (manager *Manager) Operate(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	switch operation.Action {
	case agentmodem.OperationCallStatus:
		return manager.operator.Operate(ctx, operation)
	case agentmodem.OperationCallHangup:
		result, err := manager.operator.Operate(ctx, operation)
		if err == nil && result.Call.TerminalConfirmed {
			err = manager.store.ClearTarget(operation.AttachmentID, operation.EquipmentID, operation.CardID)
			if errors.Is(err, ErrLeaseMismatch) {
				return agentmodem.OperationResult{}, err
			}
		}
		return result, err
	case agentmodem.OperationCallRenew:
		now := manager.now().UTC()
		record, err := manager.store.Renew(operation.AttachmentID, operation.EquipmentID, operation.CardID, operation.LeaseID,
			now, now.Add(leaseDuration))
		if err != nil {
			return agentmodem.OperationResult{}, err
		}
		manager.resetAttempts(record.EquipmentID)
		return agentmodem.OperationResult{LeaseID: record.LeaseID, LeaseUntil: record.ExpiresAt}, nil
	case agentmodem.OperationCallDial, agentmodem.OperationCallAnswer:
		return manager.beginCall(ctx, operation)
	default:
		return agentmodem.OperationResult{}, errors.New("unsupported paid-call operation")
	}
}

func (manager *Manager) beginCall(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	direction := "out"
	if operation.Action == agentmodem.OperationCallAnswer {
		direction = "in"
	}
	statusOperation := operation
	statusOperation.Action = agentmodem.OperationCallStatus
	statusOperation.Number = ""
	preflight, err := manager.operator.Operate(ctx, statusOperation)
	if err != nil {
		return agentmodem.OperationResult{}, err
	}
	record, created, err := manager.store.Begin(Record{
		SchemaVersion: schemaVersion, LeaseID: operation.LeaseID, OperationID: operation.OperationID,
		AttachmentID: operation.AttachmentID, EquipmentID: operation.EquipmentID, CardID: operation.CardID,
		Direction: direction, ExpiresAt: manager.now().UTC().Add(armingDuration),
	})
	if err != nil {
		return agentmodem.OperationResult{}, err
	}
	if !created {
		preflight.LeaseID, preflight.LeaseUntil = record.LeaseID, record.ExpiresAt
		return preflight, nil
	}
	manager.resetAttempts(record.EquipmentID)
	allowed := operation.Action == agentmodem.OperationCallDial && preflight.Call.State == "idle" ||
		operation.Action == agentmodem.OperationCallAnswer &&
			(preflight.Call.State == "ringing_in" || preflight.Call.State == "waiting")
	if !allowed {
		_ = manager.store.ClearTarget(operation.AttachmentID, operation.EquipmentID, operation.CardID)
		return agentmodem.OperationResult{}, agentmodem.ErrOperationUnavailable
	}
	result, err := manager.operator.Operate(ctx, operation)
	if err != nil {
		// The AT command may have reached the modem even when its response was
		// lost. Retaining the lease is the required safe outcome.
		return agentmodem.OperationResult{}, err
	}
	result.LeaseID, result.LeaseUntil = record.LeaseID, record.ExpiresAt
	return result, nil
}

func (manager *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(watchdogEvery)
	defer ticker.Stop()
	for {
		if err := manager.sweep(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (manager *Manager) sweep(ctx context.Context) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	records, err := manager.store.Records()
	if err != nil {
		return err
	}
	now := manager.now()
	for _, record := range records {
		if now.Before(record.ExpiresAt) || manager.attemptCount(record.EquipmentID) >= maximumAttempts {
			continue
		}
		manager.incrementAttempts(record.EquipmentID)
		result, operationErr := manager.operator.Operate(ctx, agentmodem.Operation{
			OperationID: "lease-expiry-" + record.LeaseID, AttachmentID: record.AttachmentID,
			EquipmentID: record.EquipmentID, CardID: record.CardID, Action: agentmodem.OperationCallHangup,
		})
		if operationErr == nil && result.Call.TerminalConfirmed {
			if clearErr := manager.store.ClearTarget(record.AttachmentID, record.EquipmentID, record.CardID); clearErr != nil {
				return clearErr
			}
			manager.resetAttempts(record.EquipmentID)
			continue
		}
		detail := "modem did not return terminal confirmation"
		if operationErr != nil {
			detail = operationErr.Error()
		}
		log.Printf("mdd-agent: paid-call safety hangup %d/%d was not confirmed for modem %s: %s",
			manager.attemptCount(record.EquipmentID), maximumAttempts, record.EquipmentID, detail)
		if manager.attemptCount(record.EquipmentID) == maximumAttempts {
			log.Printf("mdd-agent: paid-call safety hold retained for modem %s; new calls remain blocked", record.EquipmentID)
		}
	}
	return nil
}

func (manager *Manager) Close() error { return manager.store.Close() }

func (manager *Manager) attemptCount(equipmentID string) int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.attempts[equipmentID]
}

func (manager *Manager) incrementAttempts(equipmentID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.attempts[equipmentID]++
}

func (manager *Manager) resetAttempts(equipmentID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	delete(manager.attempts, equipmentID)
}
