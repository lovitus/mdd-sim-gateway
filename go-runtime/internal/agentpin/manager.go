package agentpin

import (
	"context"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type Manager struct {
	store       *Store
	runtime     agentmodem.SIMPINRuntime
	coordinator agentmodem.AuxiliaryCoordinator
	pins        map[string]string
	revisions   map[string]string
	now         func() time.Time
}

func NewManager(store *Store, runtime agentmodem.SIMPINRuntime, coordinator agentmodem.AuxiliaryCoordinator,
	pins, revisions map[string]string) (*Manager, error) {
	if store == nil || runtime == nil || coordinator == nil {
		return nil, errors.New("invalid SIM PIN manager configuration")
	}
	copyPins := make(map[string]string, len(pins))
	for cardID, pin := range pins {
		copyPins[cardID] = pin
	}
	copyRevisions := make(map[string]string, len(revisions))
	for cardID, revision := range revisions {
		copyRevisions[cardID] = revision
	}
	return &Manager{store: store, runtime: runtime, coordinator: coordinator, pins: copyPins, revisions: copyRevisions, now: time.Now}, nil
}

func (manager *Manager) RecoverPINs(ctx context.Context, facts []agentmodem.Fact) error {
	for index := range facts {
		fact := &facts[index]
		pin, configured := manager.pins[fact.SIM.ICCID]
		fact.SIM.PINConfigured = configured && pin != ""
		if fact.SIM.State != agentmodem.SIMLocked || !fact.SIM.PINConfigured {
			continue
		}
		if fact.SIM.PINState != "pin_required" || fact.SIM.PINAttempts == nil || *fact.SIM.PINAttempts < 2 {
			fact.SIM.PINRecovery = "blocked"
			continue
		}
		revision := manager.revisions[fact.SIM.ICCID]
		if revision == "" {
			revision = "legacy-config"
		}
		created, err := manager.store.Prepare(Record{
			CardID: fact.SIM.ICCID, PINRevision: revision,
			AttemptedAt: manager.now().UTC(),
		})
		if err != nil {
			fact.SIM.PINRecovery = "status_unavailable"
			continue
		}
		if !created {
			fact.SIM.PINRecovery = "blocked"
			continue
		}
		fact.SIM.PINRecovery = "attempting"
		var result agentmodem.SIMPINResult
		err = manager.coordinator.DoAuxiliary(ctx, fact.EquipmentID, func(operationContext context.Context) error {
			var operationErr error
			result, operationErr = manager.runtime.EnterSIMPIN(operationContext, agentmodem.SIMPINRequest{
				AttachmentID: fact.AttachmentID, EquipmentID: fact.EquipmentID,
				CardID: fact.SIM.ICCID, PIN: pin,
			})
			return operationErr
		})
		if err != nil && !result.Attempted {
			if clearErr := manager.store.Clear(fact.SIM.ICCID, revision); clearErr != nil {
				fact.SIM.PINRecovery = "status_unavailable"
				continue
			}
			fact.SIM.PINRecovery = "configured"
			continue
		}
		if err != nil || !result.Ready {
			fact.SIM.PINRecovery = "blocked"
			if result.AttemptsRemaining != nil {
				remaining := *result.AttemptsRemaining
				fact.SIM.PINAttempts = &remaining
			}
			continue
		}
		if err := manager.store.Clear(fact.SIM.ICCID, revision); err != nil {
			fact.SIM.PINRecovery = "status_unavailable"
			continue
		}
		fact.SIM.PINRecovery = "unlocked"
	}
	return nil
}

func (manager *Manager) Close() error { return manager.store.Close() }
