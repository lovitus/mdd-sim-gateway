// Package agentconnection shares one guarded platform bearer between a
// persistent user-requested 4G connection and an optional MDD data borrower.
// It never exposes host routes or DNS; all traffic still enters through the
// existing Agent data socket broker.
package agentconnection

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type entry struct {
	target         agentdata.Target
	requested      agentdata.Profile
	profile        string
	connected      bool
	persistent     bool
	borrowed       bool
	releasePending bool
	invalidated    <-chan struct{}
}

type Manager struct {
	backend agentdata.Backend
	mu      sync.Mutex
	items   map[string]*entry
}

func New(backend agentdata.Backend) (*Manager, error) {
	if backend == nil {
		return nil, errors.New("cellular connection backend is required")
	}
	return &Manager{backend: backend, items: map[string]*entry{}}, nil
}

func (manager *Manager) SetPersistent(ctx context.Context, target agentdata.Target, profile agentdata.Profile, enabled bool) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.items[target.EquipmentID]
	if !enabled {
		if current == nil {
			// A host-created bearer can predate this manager's ownership map.
			// The platform backend must check the exact current SIM before stop.
			return manager.backend.StopData(ctx, target)
		}
		if current.target != target {
			return agentmodem.ErrOperationTargetReplaced
		}
		if current.borrowed {
			return agentdata.ErrSessionActive
		}
		current.persistent = false
		if !current.connected && !current.releasePending {
			delete(manager.items, target.EquipmentID)
			return nil
		}
		if err := manager.backend.StopData(ctx, target); err != nil {
			return err
		}
		delete(manager.items, target.EquipmentID)
		return nil
	}
	if current != nil && current.target == target && current.connected && !current.releasePending && current.requested == profile && !agentdata.Invalidated(current.invalidated) {
		current.persistent = true
		return nil
	}
	if current != nil {
		if current.borrowed {
			return agentdata.ErrSessionActive
		}
		if current.connected || current.releasePending {
			if err := manager.backend.StopData(ctx, current.target); err != nil {
				return err
			}
		}
		delete(manager.items, target.EquipmentID)
	}
	name, err := manager.backend.PrepareData(ctx, target, profile)
	if err != nil {
		return err
	}
	invalidated, err := agentdata.DataValidity(manager.backend, target)
	manager.items[target.EquipmentID] = &entry{target: target, requested: profile, profile: name, connected: true, persistent: true, invalidated: invalidated}
	if err != nil || agentdata.Invalidated(invalidated) {
		return errors.Join(errors.New("prepared cellular claim is no longer valid"), err)
	}
	return nil
}

func (manager *Manager) PrepareData(ctx context.Context, target agentdata.Target, profile agentdata.Profile) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.items[target.EquipmentID]
	if current != nil {
		if current.target != target {
			return "", agentmodem.ErrOperationTargetReplaced
		}
		if current.borrowed || current.releasePending {
			return "", errors.New("another cellular data borrower is active")
		}
		if agentdata.Invalidated(current.invalidated) {
			return "", agentmodem.ErrOperationUnavailable
		}
		if current.connected && current.requested == profile {
			current.borrowed = true
			return current.profile, nil
		}
		if current.persistent {
			return "", errors.New("persistent cellular connection uses another profile")
		}
	}
	name, err := manager.backend.PrepareData(ctx, target, profile)
	if err != nil {
		return "", err
	}
	invalidated, err := agentdata.DataValidity(manager.backend, target)
	manager.items[target.EquipmentID] = &entry{target: target, requested: profile, profile: name, connected: true, borrowed: true, invalidated: invalidated}
	if err != nil || agentdata.Invalidated(invalidated) {
		manager.items[target.EquipmentID].borrowed = false
		return "", errors.Join(errors.New("prepared cellular claim is no longer valid"), err)
	}
	return name, nil
}

func (manager *Manager) DialData(ctx context.Context, target agentdata.Target, network, address string) (net.Conn, error) {
	manager.mu.Lock()
	current := manager.items[target.EquipmentID]
	ready := current != nil && current.target == target && current.connected && current.borrowed && !agentdata.Invalidated(current.invalidated)
	manager.mu.Unlock()
	if !ready {
		return nil, agentmodem.ErrOperationTargetReplaced
	}
	return manager.backend.DialData(ctx, target, network, address)
}

func (manager *Manager) DataValidity(target agentdata.Target) (<-chan struct{}, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.items[target.EquipmentID]
	if current == nil || current.target != target {
		return nil, agentmodem.ErrOperationTargetReplaced
	}
	return current.invalidated, nil
}

// RecoverData keeps the exact logical borrower and persistent intent while
// replacing only its disconnected bearer. Caller holds data and paid-call
// lifecycle gates and has freshly checked the SIM and observed disconnection.
func (manager *Manager) RecoverData(ctx context.Context, target agentdata.Target, profile agentdata.Profile) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.items[target.EquipmentID]
	if current == nil || current.target != target || !current.borrowed || current.requested != profile || current.releasePending {
		return agentmodem.ErrOperationTargetReplaced
	}
	// Retry partial Prepare rollback as well as the old connected claim.
	if err := manager.backend.StopData(ctx, target); err != nil {
		return err
	}
	current.connected = false
	name, err := manager.backend.PrepareData(ctx, target, profile)
	if err != nil {
		return err
	}
	current.profile, current.connected = name, true
	current.invalidated, err = agentdata.DataValidity(manager.backend, target)
	return err
}

// ReleaseBorrower transfers only logical cleanup ownership. It does not touch
// hardware and can run while voice is active. The persistent connection manager
// outlives a lost Core WSS and retains any deferred physical cleanup.
func (manager *Manager) ReleaseBorrower(target agentdata.Target) (bool, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.items[target.EquipmentID]
	if current == nil {
		return true, nil
	}
	if current.target != target {
		return false, agentmodem.ErrOperationTargetReplaced
	}
	current.borrowed = false
	if current.persistent && current.connected && !agentdata.Invalidated(current.invalidated) && !current.releasePending {
		return true, nil
	}
	current.releasePending = true
	return false, nil
}

func (manager *Manager) PendingDataCleanup() []agentdata.Target {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var result []agentdata.Target
	for _, current := range manager.items {
		if current.releasePending {
			result = append(result, current.target)
		}
	}
	return result
}

func (manager *Manager) StopData(ctx context.Context, target agentdata.Target) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.items[target.EquipmentID]
	if current == nil {
		return nil
	}
	if current.target != target {
		return agentmodem.ErrOperationTargetReplaced
	}
	current.borrowed = false
	if current.persistent && !current.releasePending {
		return nil
	}
	if err := manager.backend.StopData(ctx, target); err != nil {
		return err
	}
	delete(manager.items, target.EquipmentID)
	return nil
}

func (manager *Manager) Persistent(target agentdata.Target) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.items[target.EquipmentID]
	return current != nil && current.target == target && current.connected && current.persistent && !agentdata.Invalidated(current.invalidated)
}

// PersistentMatches observes the original owner without setting persistent or
// disturbing a borrower. Compare requested credentials, not the display name.
func (manager *Manager) PersistentMatches(target agentdata.Target, profile agentdata.Profile) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.items[target.EquipmentID]
	return current != nil && current.target == target && current.connected && current.persistent && current.requested == profile && !agentdata.Invalidated(current.invalidated)
}

func (manager *Manager) OwnedTargets() []agentdata.Target {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := make([]agentdata.Target, 0, len(manager.items))
	for _, current := range manager.items {
		result = append(result, current.target)
	}
	return result
}

func (manager *Manager) ReleaseStale(ctx context.Context, target agentdata.Target) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.items[target.EquipmentID]
	if current == nil {
		return nil
	}
	if current.target != target {
		return agentmodem.ErrOperationTargetReplaced
	}
	if current.borrowed {
		return agentdata.ErrSessionActive
	}
	if current.connected || current.releasePending {
		if err := manager.backend.StopData(ctx, target); err != nil {
			return err
		}
	}
	delete(manager.items, target.EquipmentID)
	return nil
}

var _ agentdata.Backend = (*Manager)(nil)
