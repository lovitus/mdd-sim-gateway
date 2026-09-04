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
	target     agentdata.Target
	requested  agentdata.Profile
	profile    string
	connected  bool
	persistent bool
	borrowed   bool
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
			return nil
		}
		if current.target != target {
			return agentmodem.ErrOperationTargetReplaced
		}
		if current.borrowed {
			return agentdata.ErrSessionActive
		}
		current.persistent = false
		if !current.connected {
			delete(manager.items, target.EquipmentID)
			return nil
		}
		if err := manager.backend.StopData(ctx, target); err != nil {
			return err
		}
		delete(manager.items, target.EquipmentID)
		return nil
	}
	if current != nil && current.target == target && current.connected && current.requested == profile {
		current.persistent = true
		return nil
	}
	if current != nil {
		if current.borrowed {
			return agentdata.ErrSessionActive
		}
		if current.connected {
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
	manager.items[target.EquipmentID] = &entry{target: target, requested: profile, profile: name, connected: true, persistent: true}
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
		if current.borrowed {
			return "", errors.New("another cellular data borrower is active")
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
	manager.items[target.EquipmentID] = &entry{target: target, requested: profile, profile: name, connected: true, borrowed: true}
	return name, nil
}

func (manager *Manager) DialData(ctx context.Context, target agentdata.Target, network, address string) (net.Conn, error) {
	manager.mu.Lock()
	current := manager.items[target.EquipmentID]
	ready := current != nil && current.target == target && current.connected && current.borrowed
	manager.mu.Unlock()
	if !ready {
		return nil, agentmodem.ErrOperationTargetReplaced
	}
	return manager.backend.DialData(ctx, target, network, address)
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
	if current.persistent {
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
	return current != nil && current.target == target && current.connected && current.persistent
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
	if current.connected {
		if err := manager.backend.StopData(ctx, target); err != nil {
			return err
		}
	}
	delete(manager.items, target.EquipmentID)
	return nil
}

var _ agentdata.Backend = (*Manager)(nil)
