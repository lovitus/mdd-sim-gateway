package agentat

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Target struct {
	AttachmentID string
	EquipmentID  string
}

type Snapshot struct {
	State          string
	Port           string
	Detail         string
	CallSignalling bool
	SMS            bool
	SIMAPDU        bool
}

type Enumerator func() ([]Candidate, error)

type managedOwner struct {
	owner        *Owner
	lastHealthAt time.Time
}

// Manager reconciles currently observed MBN equipment to exactly one retained
// auxiliary AT handle per equipment ID. A transient enumeration failure does
// not close a known-good exclusive handle, but the published state degrades
// until enumeration succeeds again.
type Manager struct {
	mu          sync.Mutex
	enumerate   Enumerator
	open        Opener
	healthEvery time.Duration
	owners      map[string]*managedOwner
}

func NewManager(enumerate Enumerator, open Opener) (*Manager, error) {
	if enumerate == nil || open == nil {
		return nil, errors.New("invalid AT ownership manager configuration")
	}
	return &Manager{
		enumerate: enumerate, open: open, healthEvery: 10 * time.Second,
		owners: make(map[string]*managedOwner),
	}, nil
}

func (manager *Manager) Reconcile(ctx context.Context, targets []Target) map[string]Snapshot {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := make(map[string]Snapshot, len(targets))
	desired, duplicates := normalizeTargets(targets)
	for attachmentID, detail := range duplicates {
		result[attachmentID] = Snapshot{State: "degraded", Detail: detail}
	}
	for equipmentID, owned := range manager.owners {
		if _, exists := desired[equipmentID]; !exists {
			_ = owned.owner.Close()
			delete(manager.owners, equipmentID)
		}
	}
	candidates, err := manager.enumerate()
	if err != nil {
		detail := boundedDetail(fmt.Sprintf("enumerate AT control ports: %v", err))
		for _, attachmentID := range desired {
			if _, duplicate := duplicates[attachmentID]; !duplicate {
				result[attachmentID] = Snapshot{State: "degraded", Detail: detail}
			}
		}
		return result
	}
	present := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		present[strings.ToLower(candidate.Name)] = struct{}{}
	}
	for equipmentID, owned := range manager.owners {
		if _, exists := present[strings.ToLower(owned.owner.Name())]; !exists {
			_ = owned.owner.Close()
			delete(manager.owners, equipmentID)
		}
	}
	claimed := make(map[string]struct{}, len(manager.owners))
	for _, owned := range manager.owners {
		claimed[strings.ToLower(owned.owner.Name())] = struct{}{}
	}
	now := time.Now()
	for equipmentID, attachmentID := range desired {
		if _, duplicate := duplicates[attachmentID]; duplicate {
			continue
		}
		owned := manager.owners[equipmentID]
		if owned != nil && now.Sub(owned.lastHealthAt) >= manager.healthEvery {
			healthContext, cancel := context.WithTimeout(ctx, 3*time.Second)
			healthErr := owned.owner.Healthy(healthContext)
			cancel()
			if healthErr != nil {
				delete(claimed, strings.ToLower(owned.owner.Name()))
				_ = owned.owner.Close()
				delete(manager.owners, equipmentID)
				owned = nil
			} else {
				owned.lastHealthAt = now
			}
		}
		if owned == nil {
			available := make([]Candidate, 0, len(candidates))
			for _, candidate := range candidates {
				if _, exists := claimed[strings.ToLower(candidate.Name)]; !exists {
					available = append(available, candidate)
				}
			}
			discoveryContext, cancel := context.WithTimeout(ctx, 15*time.Second)
			owner, discoverErr := Discover(discoveryContext, equipmentID, available, manager.open)
			cancel()
			if discoverErr != nil {
				result[attachmentID] = discoverySnapshot(discoverErr)
				continue
			}
			owned = &managedOwner{owner: owner, lastHealthAt: now}
			manager.owners[equipmentID] = owned
			claimed[strings.ToLower(owner.Name())] = struct{}{}
		}
		capabilities := owned.owner.Capabilities()
		result[attachmentID] = Snapshot{
			State: "ready", Port: owned.owner.Name(),
			CallSignalling: capabilities.CallSignalling, SMS: capabilities.SMS,
			SIMAPDU: capabilities.SIMAPDU,
		}
	}
	return result
}

func (manager *Manager) Close() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	errorsSeen := make([]error, 0, len(manager.owners))
	for equipmentID, owned := range manager.owners {
		if err := owned.owner.Close(); err != nil {
			errorsSeen = append(errorsSeen, err)
		}
		delete(manager.owners, equipmentID)
	}
	return errors.Join(errorsSeen...)
}

func (manager *Manager) CallStatus(ctx context.Context, equipmentID string) (CallState, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return CallState{}, err
	}
	return owned.CallStatus(ctx)
}

func (manager *Manager) VerifiedHangup(ctx context.Context, equipmentID string) (CallState, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return CallState{}, err
	}
	return owned.VerifiedHangup(ctx)
}

func (manager *Manager) Dial(ctx context.Context, equipmentID, number string) (CallState, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return CallState{}, err
	}
	return owned.Dial(ctx, number)
}

func (manager *Manager) Answer(ctx context.Context, equipmentID string) (CallState, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return CallState{}, err
	}
	return owned.Answer(ctx)
}

func (manager *Manager) callOwner(equipmentID string) (*Owner, error) {
	if !equipmentIDPattern.MatchString(equipmentID) {
		return nil, errors.New("invalid modem equipment identity")
	}
	owned := manager.owners[equipmentID]
	if owned == nil {
		return nil, errors.New("AT control owner is unavailable")
	}
	if !owned.owner.Capabilities().CallSignalling {
		return nil, errors.New("AT control owner has no call signalling capability")
	}
	return owned.owner, nil
}

func normalizeTargets(input []Target) (map[string]string, map[string]string) {
	sorted := append([]Target(nil), input...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].AttachmentID < sorted[right].AttachmentID })
	desired := make(map[string]string, len(sorted))
	duplicates := make(map[string]string)
	byEquipment := make(map[string][]string, len(sorted))
	for _, target := range sorted {
		if target.AttachmentID == "" {
			continue
		}
		if !equipmentIDPattern.MatchString(target.EquipmentID) {
			duplicates[target.AttachmentID] = "MBN did not provide a usable equipment identity"
			continue
		}
		byEquipment[target.EquipmentID] = append(byEquipment[target.EquipmentID], target.AttachmentID)
	}
	for equipmentID, attachments := range byEquipment {
		if len(attachments) == 1 {
			desired[equipmentID] = attachments[0]
			continue
		}
		for _, attachmentID := range attachments {
			duplicates[attachmentID] = "multiple MBN attachments reported the same equipment identity"
		}
	}
	return desired, duplicates
}

func discoverySnapshot(err error) Snapshot {
	if errors.Is(err, context.DeadlineExceeded) {
		return Snapshot{State: "degraded", Detail: "AT control discovery timed out"}
	}
	var discovery DiscoveryError
	if errors.As(err, &discovery) && discovery.Busy {
		return Snapshot{State: "busy", Detail: discovery.Error()}
	}
	if errors.As(err, &discovery) && discovery.OpenFailures > 0 && discovery.Opened == 0 {
		return Snapshot{State: "degraded", Detail: boundedDetail(discovery.Error())}
	}
	return Snapshot{State: "unavailable", Detail: boundedDetail(err.Error())}
}

func boundedDetail(value string) string {
	value = strings.ToValidUTF8(value, "?")
	if len(value) > 1024 {
		value = strings.ToValidUTF8(value[:1024], "?")
	}
	return value
}
