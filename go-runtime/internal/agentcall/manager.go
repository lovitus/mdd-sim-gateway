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
	armingDuration     = 30 * time.Second
	leaseDuration      = 10 * time.Second
	watchdogEvery      = 500 * time.Millisecond
	hangupRetryBase    = time.Second
	hangupRetryMaximum = 30 * time.Second
)

var ErrAuxiliaryDuringCall = errors.New("modem auxiliary operation is blocked by a paid-call lease")

// Manager is the only paid-call operator exposed by the Agent. The durable
// record is committed before ATD/ATA and is removed only after fresh CLCC
// samples confirm that the physical modem is idle.
type Manager struct {
	store    *Store
	operator agentmodem.Operator
	now      func() time.Time
	incoming agentmodem.IncomingCallVerifier

	operationMu sync.Mutex
	mu          sync.Mutex
	attempts    map[string]hangupAttempt
}

type hangupAttempt struct {
	count uint32
	next  time.Time
}

func NewManager(store *Store, operator agentmodem.Operator) (*Manager, error) {
	if store == nil || operator == nil {
		return nil, errors.New("invalid paid-call manager configuration")
	}
	return &Manager{store: store, operator: operator, now: time.Now, attempts: map[string]hangupAttempt{}}, nil
}

func (manager *Manager) BindIncomingCallVerifier(verifier agentmodem.IncomingCallVerifier) error {
	if manager == nil || verifier == nil {
		return errors.New("incoming call verifier is required")
	}
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	if manager.incoming != nil {
		return errors.New("incoming call verifier is already bound")
	}
	manager.incoming = verifier
	return nil
}

func (manager *Manager) Operate(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	switch operation.Action {
	case agentmodem.OperationSMSList, agentmodem.OperationSMSSend:
		if err := manager.auxiliaryAllowedLocked(operation.EquipmentID); err != nil {
			return agentmodem.OperationResult{}, err
		}
		return manager.operator.Operate(ctx, operation)
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
	case agentmodem.OperationCallDTMF:
		if _, err := manager.store.Require(operation.AttachmentID, operation.EquipmentID,
			operation.CardID, operation.LeaseID, manager.now().UTC()); err != nil {
			return agentmodem.OperationResult{}, err
		}
		return manager.operator.Operate(ctx, operation)
	case agentmodem.OperationCallDial, agentmodem.OperationCallAnswer:
		return manager.beginCall(ctx, operation)
	case agentmodem.OperationCallReject:
		return manager.rejectIncoming(ctx, operation)
	default:
		return agentmodem.OperationResult{}, errors.New("unsupported paid-call operation")
	}
}

// DoBackgroundScan is the sole scanner admission boundary. It holds the same
// global operation lock as renew/watchdog/hangup and refuses every scan while
// any paid-call lease exists, including a lease on another modem.
func (manager *Manager) DoBackgroundScan(ctx context.Context, callback func(context.Context) error) error {
	if callback == nil {
		return errors.New("nil modem background scan")
	}
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	records, err := manager.store.Records()
	if err != nil {
		return err
	}
	if len(records) != 0 {
		return ErrAuxiliaryDuringCall
	}
	return callback(ctx)
}

// DoAuxiliary serializes one non-call operation with every paid-call command
// and the lease watchdog. It deliberately holds the same operation lock while
// callback runs, so a SIM APDU sequence cannot interleave with dial/SMS/hangup.
func (manager *Manager) DoAuxiliary(ctx context.Context, equipmentID string, callback func(context.Context) error) error {
	if callback == nil {
		return errors.New("nil modem auxiliary operation")
	}
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	if err := manager.auxiliaryAllowedLocked(equipmentID); err != nil {
		return err
	}
	return callback(ctx)
}

func (manager *Manager) auxiliaryAllowedLocked(equipmentID string) error {
	records, err := manager.store.Records()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.EquipmentID == equipmentID {
			return ErrAuxiliaryDuringCall
		}
	}
	return nil
}

func (manager *Manager) beginCall(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	direction := "out"
	if operation.Action == agentmodem.OperationCallAnswer {
		direction = "in"
		if err := manager.requireIncoming(operation); err != nil {
			return agentmodem.OperationResult{}, err
		}
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
			preflight.Call.State == "ringing_in" && preflight.Call.Direction == "in" &&
			preflight.Call.NativeIndex == operation.NativeCallIndex && preflight.Call.VoiceCalls == 1 &&
			preflight.Call.IncomingCalls == 1
	if !allowed {
		_ = manager.store.ClearTarget(operation.AttachmentID, operation.EquipmentID, operation.CardID)
		return agentmodem.OperationResult{}, agentmodem.ErrOperationUnavailable
	}
	result, err := manager.operator.Operate(ctx, operation)
	if err != nil {
		if errors.Is(err, agentmodem.ErrIncomingCallChanged) || errors.Is(err, agentmodem.ErrOperationTargetReplaced) ||
			errors.Is(err, agentmodem.ErrOperationUnavailable) {
			_ = manager.store.ClearTarget(operation.AttachmentID, operation.EquipmentID, operation.CardID)
		}
		// Fresh target/incoming mismatches occur before ATA and release the
		// arming lease above. Every other failure may be post-write, so retaining
		// the lease is the required safe outcome.
		return agentmodem.OperationResult{}, err
	}
	now := manager.now().UTC()
	record, err = manager.store.Renew(operation.AttachmentID, operation.EquipmentID, operation.CardID,
		operation.LeaseID, now, now.Add(leaseDuration))
	if err != nil {
		// The paid call may already be live. Preserve the durable arming record
		// and surface uncertainty so Core stops media/renewal and requests hangup.
		return agentmodem.OperationResult{}, err
	}
	result.LeaseID, result.LeaseUntil = record.LeaseID, record.ExpiresAt
	return result, nil
}

func (manager *Manager) rejectIncoming(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	if err := manager.requireIncoming(operation); err != nil {
		return agentmodem.OperationResult{}, err
	}
	records, err := manager.store.Records()
	if err != nil {
		return agentmodem.OperationResult{}, err
	}
	if len(records) != 0 {
		return agentmodem.OperationResult{}, ErrAuxiliaryDuringCall
	}
	return manager.operator.Operate(ctx, operation)
}

func (manager *Manager) requireIncoming(operation agentmodem.Operation) error {
	if manager.incoming == nil {
		return agentmodem.ErrOperationUnavailable
	}
	return manager.incoming.RequireIncomingCall(agentmodem.IncomingCallFence{
		EventID: operation.IncomingEventID, AttachmentID: operation.AttachmentID,
		EquipmentID: operation.EquipmentID, CardID: operation.CardID,
		SIMSessionGeneration: operation.SIMSessionGeneration,
		NativeCallIndex:      operation.NativeCallIndex, CallOccurrence: operation.CallOccurrence,
		Number: operation.Number,
	})
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
		if now.Before(record.ExpiresAt) || !manager.hangupDue(record.EquipmentID, now) {
			continue
		}
		attempt := manager.beginHangupAttempt(record.EquipmentID, now)
		result, operationErr := manager.operator.Operate(ctx, agentmodem.Operation{
			OperationID: "lease-expiry-" + record.LeaseID, AttachmentID: record.AttachmentID,
			EquipmentID: record.EquipmentID, CardID: record.CardID, Action: agentmodem.OperationCallHangup,
		})
		if operationErr == nil && result.Call.TerminalConfirmed {
			if clearErr := manager.store.ClearTarget(record.AttachmentID, record.EquipmentID, record.CardID); clearErr != nil {
				manager.deferHangupAttempt(record.EquipmentID, now, attempt)
				log.Printf("mdd-agent: paid-call terminal hangup was confirmed for modem %s but durable lease clear failed: %v; retrying in %s",
					record.EquipmentID, clearErr, attempt.delay)
				continue
			}
			manager.resetAttempts(record.EquipmentID)
			continue
		}
		detail := "modem did not return terminal confirmation"
		if operationErr != nil {
			detail = operationErr.Error()
		}
		manager.deferHangupAttempt(record.EquipmentID, now, attempt)
		log.Printf("mdd-agent: paid-call safety hangup %d was not confirmed for modem %s: %s; durable hold retained and retrying in %s",
			attempt.count, record.EquipmentID, detail, attempt.delay)
	}
	return nil
}

func (manager *Manager) Close() error { return manager.store.Close() }

type pendingHangupAttempt struct {
	count uint32
	delay time.Duration
}

func (manager *Manager) hangupDue(equipmentID string, now time.Time) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return !now.Before(manager.attempts[equipmentID].next)
}

func (manager *Manager) beginHangupAttempt(equipmentID string, now time.Time) pendingHangupAttempt {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.attempts[equipmentID]
	state.count++
	delay := hangupRetryBase
	for count := uint32(1); count < state.count && delay < hangupRetryMaximum; count++ {
		delay *= 2
		if delay > hangupRetryMaximum {
			delay = hangupRetryMaximum
		}
	}
	// Mark this attempt in progress. The operation lock already prevents a
	// concurrent sweep; setting next now also keeps introspection deterministic.
	state.next = now
	manager.attempts[equipmentID] = state
	return pendingHangupAttempt{count: state.count, delay: delay}

}

func (manager *Manager) deferHangupAttempt(equipmentID string, now time.Time, attempt pendingHangupAttempt) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.attempts[equipmentID]
	if state.count == attempt.count {
		state.next = now.Add(attempt.delay)
		manager.attempts[equipmentID] = state
	}
}

func (manager *Manager) resetAttempts(equipmentID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	delete(manager.attempts, equipmentID)
}
