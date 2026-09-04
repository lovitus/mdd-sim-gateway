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
	State           string
	Port            string
	Detail          string
	OwnerGeneration uint64
	CallSignalling  bool
	SMS             bool
	SIMAPDU         bool
}

type Enumerator func() ([]Candidate, error)

type managedOwner struct {
	owner        *Owner
	generation   uint64
	lastHealthAt time.Time
	pinStatus    SIMPINStatus
	pinStatusAt  time.Time
}

// Manager reconciles currently observed MBN equipment to exactly one retained
// auxiliary AT handle per equipment ID. A transient enumeration failure does
// not close a known-good exclusive handle, but the published state degrades
// until enumeration succeeds again.
type Manager struct {
	mu             sync.Mutex
	enumerate      Enumerator
	open           Opener
	healthEvery    time.Duration
	simAPDU        bool
	owners         map[string]*managedOwner
	nextGeneration uint64
}

func NewManager(enumerate Enumerator, open Opener) (*Manager, error) {
	return NewManagerWithSIMAPDU(enumerate, open, false)
}

func NewManagerWithSIMAPDU(enumerate Enumerator, open Opener, simAPDU bool) (*Manager, error) {
	if enumerate == nil || open == nil {
		return nil, errors.New("invalid AT ownership manager configuration")
	}
	return &Manager{
		enumerate: enumerate, open: open, healthEvery: 10 * time.Second, simAPDU: simAPDU,
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
			owner, discoverErr := discover(discoveryContext, equipmentID, available, manager.open, manager.simAPDU)
			cancel()
			if discoverErr != nil {
				result[attachmentID] = discoverySnapshot(discoverErr)
				continue
			}
			manager.nextGeneration++
			owned = &managedOwner{owner: owner, generation: manager.nextGeneration, lastHealthAt: now}
			manager.owners[equipmentID] = owned
			claimed[strings.ToLower(owner.Name())] = struct{}{}
		}
		capabilities := owned.owner.Capabilities()
		result[attachmentID] = Snapshot{
			State: "ready", Port: owned.owner.Name(), OwnerGeneration: owned.generation,
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

func (manager *Manager) CallInventory(ctx context.Context, equipmentID string) (CallInventory, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return CallInventory{}, err
	}
	return owned.CallInventory(ctx)
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

func (manager *Manager) AnswerIncoming(ctx context.Context, equipmentID string, expected IncomingCallFence) (CallState, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return CallState{}, err
	}
	return owned.AnswerIncoming(ctx, expected)
}

func (manager *Manager) RejectIncoming(ctx context.Context, equipmentID string, expected IncomingCallFence) (CallState, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return CallState{}, err
	}
	return owned.RejectIncoming(ctx, expected)
}

func (manager *Manager) SendDTMF(ctx context.Context, equipmentID, signal string) (CallState, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return CallState{}, err
	}
	return owned.SendDTMF(ctx, signal)
}

func (manager *Manager) ListSMS(ctx context.Context, equipmentID string) ([]SMSMessage, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.smsOwner(equipmentID)
	if err != nil {
		return nil, err
	}
	return owned.ListSMS(ctx, equipmentID)
}

func (manager *Manager) SendSMS(ctx context.Context, equipmentID, recipient, body string) ([]int, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.smsOwner(equipmentID)
	if err != nil {
		return nil, err
	}
	return owned.SendSMS(ctx, equipmentID, recipient, body)
}

func (manager *Manager) DeleteSMS(ctx context.Context, equipmentID string, indices []int, fingerprint string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.smsOwner(equipmentID)
	if err != nil {
		return err
	}
	return owned.DeleteSMS(ctx, equipmentID, indices, fingerprint)
}

func (manager *Manager) AuthenticateAKA(ctx context.Context, equipmentID, application string, rand16, autn16 []byte) (SIMAKAResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.simAPDUOwner(equipmentID)
	if err != nil {
		return SIMAKAResult{}, err
	}
	return owned.AuthenticateAKA(ctx, application, rand16, autn16)
}

func (manager *Manager) SIMPINStatus(ctx context.Context, equipmentID string) (SIMPINStatus, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.anyOwner(equipmentID)
	if err != nil {
		return SIMPINStatus{}, err
	}
	if !owned.pinStatusAt.IsZero() && time.Since(owned.pinStatusAt) < 3*time.Second {
		return cloneSIMPINStatus(owned.pinStatus), nil
	}
	status, err := owned.owner.SIMPINStatus(ctx)
	if err == nil {
		owned.pinStatus, owned.pinStatusAt = cloneSIMPINStatus(status), time.Now()
	}
	return status, err
}

// SIMPINStatusFresh bypasses the short presentation cache. Paid-operation and
// media admission use it to re-prove the card identity immediately before an
// action, including a card swap that does not change the modem attachment.
func (manager *Manager) SIMPINStatusFresh(ctx context.Context, equipmentID string) (SIMPINStatus, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.anyOwner(equipmentID)
	if err != nil {
		return SIMPINStatus{}, err
	}
	status, err := owned.owner.SIMPINStatus(ctx)
	if err == nil {
		owned.pinStatus, owned.pinStatusAt = cloneSIMPINStatus(status), time.Now()
	} else {
		owned.pinStatusAt = time.Time{}
	}
	return status, err
}

func (manager *Manager) InvalidateSIMPINStatus(equipmentID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if owned := manager.owners[equipmentID]; owned != nil {
		owned.pinStatusAt = time.Time{}
	}
}

func (manager *Manager) SIMPINStatusFull(ctx context.Context, equipmentID string) (SIMPINStatus, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.anyOwner(equipmentID)
	if err != nil {
		return SIMPINStatus{}, err
	}
	status, err := owned.owner.SIMPINStatusFull(ctx)
	if err == nil {
		owned.pinStatus, owned.pinStatusAt = cloneSIMPINStatus(status), time.Now()
	}
	return status, err
}

func (manager *Manager) EnterSIMPIN(ctx context.Context, equipmentID, cardID, pin string) (SIMPINResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.anyOwner(equipmentID)
	if err != nil {
		return SIMPINResult{}, err
	}
	result, err := owned.owner.EnterSIMPIN(ctx, cardID, pin)
	if result.Status.State != "" {
		owned.pinStatus, owned.pinStatusAt = cloneSIMPINStatus(result.Status), time.Now()
	} else {
		owned.pinStatusAt = time.Time{}
	}
	return result, err
}

func (manager *Manager) PhysicalID(equipmentID string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.anyOwner(equipmentID)
	if err != nil {
		return "", err
	}
	if owned.owner.PhysicalID() == "" {
		return "", errors.New("AT control owner has no physical parent identity")
	}
	return owned.owner.PhysicalID(), nil
}

// ReleaseForRawUSB closes exactly one retained AT handle and returns its
// already-proved physical USB parent. The platform prober calls this while it
// owns its topology lock, immediately before sing-usbip captures that parent;
// a periodic reconcile therefore cannot reacquire a child function in between.
func (manager *Manager) ReleaseForRawUSB(equipmentID string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.anyOwner(equipmentID)
	if err != nil {
		return "", err
	}
	physicalID := owned.owner.PhysicalID()
	if physicalID == "" {
		return "", errors.New("AT control owner has no physical parent identity")
	}
	delete(manager.owners, equipmentID)
	if err := owned.owner.Close(); err != nil {
		return "", fmt.Errorf("release AT control owner for raw USB: %w", err)
	}
	return physicalID, nil
}

// Exchange runs one already validated AT transaction through the retained
// exclusive owner. Platform adapters use it for read-only topology facts; it
// does not expose raw AT through the Agent protocol.
func (manager *Manager) Exchange(ctx context.Context, equipmentID, command string, timeout time.Duration) ([]byte, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.anyOwner(equipmentID)
	if err != nil {
		return nil, err
	}
	return owned.owner.Exchange(ctx, command, timeout)
}

func (manager *Manager) EnableVoicePCM(ctx context.Context, equipmentID string) error {
	return manager.EnableVoicePCMMode(ctx, equipmentID, 0)
}

func (manager *Manager) EnableVoicePCMMode(ctx context.Context, equipmentID string, mode int) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return err
	}
	return owned.EnableVoicePCMMode(ctx, mode)
}

func (manager *Manager) DisableVoicePCM(ctx context.Context, equipmentID string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	owned, err := manager.callOwner(equipmentID)
	if err != nil {
		return err
	}
	return owned.DisableVoicePCM(ctx)
}

func (manager *Manager) callOwner(equipmentID string) (*Owner, error) {
	owned, err := manager.anyOwner(equipmentID)
	if err != nil {
		return nil, err
	}
	if !owned.owner.Capabilities().CallSignalling {
		return nil, errors.New("AT control owner has no call signalling capability")
	}
	return owned.owner, nil
}

func (manager *Manager) anyOwner(equipmentID string) (*managedOwner, error) {
	if !equipmentIDPattern.MatchString(equipmentID) {
		return nil, errors.New("invalid modem equipment identity")
	}
	owned := manager.owners[equipmentID]
	if owned == nil {
		return nil, errors.New("AT control owner is unavailable")
	}
	return owned, nil
}

func (manager *Manager) smsOwner(equipmentID string) (*Owner, error) {
	if !equipmentIDPattern.MatchString(equipmentID) {
		return nil, errors.New("invalid modem equipment identity")
	}
	owned := manager.owners[equipmentID]
	if owned == nil {
		return nil, errors.New("AT control owner is unavailable")
	}
	if !owned.owner.Capabilities().SMS {
		return nil, errors.New("AT control owner has no SMS capability")
	}
	return owned.owner, nil
}

func (manager *Manager) simAPDUOwner(equipmentID string) (*Owner, error) {
	if !equipmentIDPattern.MatchString(equipmentID) {
		return nil, errors.New("invalid modem equipment identity")
	}
	owned := manager.owners[equipmentID]
	if owned == nil {
		return nil, errors.New("AT control owner is unavailable")
	}
	if !owned.owner.Capabilities().SIMAPDU {
		return nil, errors.New("AT control owner has no SIM APDU capability")
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

func cloneSIMPINStatus(status SIMPINStatus) SIMPINStatus {
	copy := status
	if status.AttemptsRemaining != nil {
		remaining := *status.AttemptsRemaining
		copy.AttemptsRemaining = &remaining
	}
	return copy
}
