package agentmodem

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sync"
)

type simInsertion struct {
	equipmentID string
	cardID      string
	epoch       string
	generation  string
}

// SIMInsertionTracker assigns one opaque generation to one continuously
// observed SIM insertion. Call Observe only after a successful, authoritative
// platform inventory. Failed probes must return their error instead of an
// empty observation; callers then fail closed without inventing an absence.
type SIMInsertionTracker struct {
	mu       sync.Mutex
	salt     [32]byte
	counter  uint64
	sessions map[string]simInsertion
}

func NewSIMInsertionTracker() (*SIMInsertionTracker, error) {
	tracker := &SIMInsertionTracker{sessions: make(map[string]simInsertion)}
	if _, err := rand.Read(tracker.salt[:]); err != nil {
		return nil, err
	}
	return tracker, nil
}

// Observe returns a detached copy. A ready SIM without a platform continuity
// epoch is intentionally left unfenced; built-in platform Probers must always
// provide that epoch. Duplicate attachment facts are also left unfenced.
func (tracker *SIMInsertionTracker) Observe(source []Fact) []Fact {
	facts := cloneSessionFacts(source)
	counts := make(map[string]int, len(facts))
	for _, fact := range facts {
		if fact.AttachmentID != "" {
			counts[fact.AttachmentID]++
		}
	}

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	next := make(map[string]simInsertion, len(facts))
	for index := range facts {
		fact := &facts[index]
		fact.SessionGenerationAuthority = true
		fact.SIM.SessionGeneration = ""
		if counts[fact.AttachmentID] != 1 || fact.EquipmentID == "" || fact.ContinuityEpoch == "" ||
			fact.SIM.State != SIMReady || fact.SIM.ICCID == "" {
			continue
		}
		current, exists := tracker.sessions[fact.AttachmentID]
		if !exists || current.equipmentID != fact.EquipmentID || current.cardID != fact.SIM.ICCID ||
			current.epoch != fact.ContinuityEpoch {
			current = simInsertion{
				equipmentID: fact.EquipmentID, cardID: fact.SIM.ICCID, epoch: fact.ContinuityEpoch,
				generation: tracker.nextGeneration(fact.AttachmentID, fact.EquipmentID, fact.SIM.ICCID, fact.ContinuityEpoch),
			}
		}
		fact.SIM.SessionGeneration = current.generation
		next[fact.AttachmentID] = current
	}
	tracker.sessions = next
	return facts
}

// Project applies the current insertion fence to a fresh auxiliary probe. It
// never creates a generation, but confirmed absence or replacement retires an
// old generation so a stale proof cannot become valid again. Partial/failed
// identity observations remain non-operable without mutating tracker state.
func (tracker *SIMInsertionTracker) Project(source []Fact) []Fact {
	facts := cloneSessionFacts(source)
	counts := make(map[string]int, len(facts))
	for _, fact := range facts {
		if fact.AttachmentID != "" {
			counts[fact.AttachmentID]++
		}
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	seen := make(map[string]struct{}, len(facts))
	for index := range facts {
		fact := &facts[index]
		fact.SessionGenerationAuthority = true
		fact.SIM.SessionGeneration = ""
		seen[fact.AttachmentID] = struct{}{}
		current, exists := tracker.sessions[fact.AttachmentID]
		if !exists {
			continue
		}
		if counts[fact.AttachmentID] != 1 {
			delete(tracker.sessions, fact.AttachmentID)
			continue
		}
		if fact.EquipmentID == "" || fact.ContinuityEpoch == "" {
			continue
		}
		if fact.EquipmentID != current.equipmentID || fact.ContinuityEpoch != current.epoch ||
			fact.SIM.State == SIMAbsent || fact.SIM.State == SIMReady && fact.SIM.ICCID != "" && fact.SIM.ICCID != current.cardID {
			delete(tracker.sessions, fact.AttachmentID)
			continue
		}
		if fact.SIM.State == SIMReady && fact.SIM.ICCID == current.cardID {
			fact.SIM.SessionGeneration = current.generation
		}
	}
	for attachmentID := range tracker.sessions {
		if _, exists := seen[attachmentID]; !exists {
			delete(tracker.sessions, attachmentID)
		}
	}
	return facts
}

// Invalidate marks continuity unknown after a failed or partial platform
// observation. The next authoritative ready fact starts a new insertion.
func (tracker *SIMInsertionTracker) Invalidate() {
	tracker.mu.Lock()
	tracker.sessions = make(map[string]simInsertion)
	tracker.mu.Unlock()
}

func (tracker *SIMInsertionTracker) nextGeneration(attachmentID, equipmentID, cardID, epoch string) string {
	tracker.counter++
	hash := sha256.New()
	_, _ = hash.Write(tracker.salt[:])
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], tracker.counter)
	_, _ = hash.Write(counter[:])
	_, _ = hash.Write([]byte(attachmentID + "\x00" + equipmentID + "\x00" + cardID + "\x00" + epoch))
	return hex.EncodeToString(hash.Sum(nil))
}

func cloneSessionFacts(source []Fact) []Fact {
	result := make([]Fact, len(source))
	copy(result, source)
	for index := range result {
		result[index].SIM.MSISDNs = append([]string(nil), source[index].SIM.MSISDNs...)
		result[index].SIM.PINAttempts = cloneSessionUint32(source[index].SIM.PINAttempts)
		result[index].Network.SignalPercent = cloneSessionUint32(source[index].Network.SignalPercent)
	}
	return result
}

func cloneSessionUint32(source *uint32) *uint32 {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}
