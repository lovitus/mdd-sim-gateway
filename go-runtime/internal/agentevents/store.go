// Package agentevents owns the Agent-side durable cellular business-event
// cursor and outbox. It does not perform AT I/O; Scanner supplies fresh,
// fenced observations and AgentLink supplies acknowledgements.
package agentevents

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	bolt "go.etcd.io/bbolt"
)

const schemaVersion = 1

var (
	bucketMeta        = []byte("meta")
	bucketSMSBaseline = []byte("sms-baseline-v1")
	bucketSMSSeen     = []byte("sms-seen-v1")
	bucketCallCursor  = []byte("call-cursor-v1")
	bucketOutbox      = []byte("event-outbox-v1")
	bucketOutboxIDs   = []byte("event-outbox-ids-v1")
	bucketSMSDelete   = []byte("sms-delete-v1")
	keySchema         = []byte("schema")
)

type Fence struct {
	AttachmentID         string
	EquipmentID          string
	CardID               string
	SIMSessionGeneration string
}

type callCursor struct {
	Fence
	Occurrence      uint64    `json:"occurrence"`
	IncomingEventID string    `json:"incoming_event_id,omitempty"`
	NativeIndex     int       `json:"native_call_index,omitempty"`
	State           string    `json:"state"`
	Number          string    `json:"number,omitempty"`
	FirstObservedAt time.Time `json:"first_observed_at,omitempty"`
	LastObservedAt  time.Time `json:"last_observed_at"`
	LastEmittedAt   time.Time `json:"last_emitted_at,omitempty"`
	Revision        uint64    `json:"revision"`
	HadIdle         bool      `json:"had_idle"`
}

type SMSDeletion struct {
	Fence
	Fingerprint string    `json:"fingerprint"`
	Indices     []int     `json:"indices"`
	QueuedAt    time.Time `json:"queued_at"`
}

type Store struct {
	db             *bolt.DB
	wake           chan struct{}
	ready          atomic.Bool
	validatedMu    sync.RWMutex
	validatedCalls map[string]struct{}
	closeOnce      sync.Once
	closeErr       error
}

func Open(path string, timeout time.Duration) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" || timeout <= 0 {
		return nil, errors.New("invalid Agent event store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, wake: make(chan struct{}, 1), validatedCalls: make(map[string]struct{})}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketSMSBaseline, bucketSMSSeen, bucketCallCursor, bucketOutbox, bucketOutboxIDs, bucketSMSDelete} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		meta := tx.Bucket(bucketMeta)
		stored := meta.Get(keySchema)
		if stored == nil {
			var wire [8]byte
			binary.BigEndian.PutUint64(wire[:], schemaVersion)
			return meta.Put(keySchema, wire[:])
		}
		if len(stored) != 8 || binary.BigEndian.Uint64(stored) != schemaVersion {
			return errors.New("unsupported Agent event store schema")
		}
		return nil
	}); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

func (store *Store) MarkReady() {
	store.ready.Store(true)
	store.notify()
}

func (store *Store) ObserveSMS(fence Fence, messages []agentmodem.SMSMessage, now time.Time) error {
	if !validFence(fence) || now.IsZero() {
		return errors.New("invalid Agent SMS event observation")
	}
	now = now.UTC()
	inserted := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		baseline := tx.Bucket(bucketSMSBaseline)
		seen := tx.Bucket(bucketSMSSeen)
		first := baseline.Get([]byte(fence.CardID)) == nil
		for _, message := range messages {
			if message.Fingerprint == "" {
				continue
			}
			key := smsSeenKey(fence.CardID, message.Fingerprint)
			if seen.Get(key) != nil {
				continue
			}
			if err := seen.Put(key, []byte{1}); err != nil {
				return err
			}
			if message.State == "received" && !displayableSMS(message.Body) {
				continue
			}
			if first || message.State != "received" && message.State != "delivery" {
				continue
			}
			event := smsEvent(fence, message, now)
			if event.Validate() != nil {
				continue
			}
			created, err := enqueue(tx, event)
			if err != nil {
				return err
			}
			inserted = inserted || created
		}
		if first {
			return baseline.Put([]byte(fence.CardID), []byte{1})
		}
		return nil
	})
	if err == nil && inserted {
		store.notify()
	}
	return err
}

func (store *Store) ObserveCall(fence Fence, call agentmodem.CallResult, now time.Time) error {
	if !validFence(fence) || now.IsZero() || !call.Authoritative || call.ObservedAt.IsZero() {
		return errors.New("invalid Agent call event observation")
	}
	now = now.UTC()
	inserted := false
	validated := ""
	invalidated := []string{}
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketCallCursor)
		cursor, found, err := decodeCursor(bucket.Get([]byte(fence.EquipmentID)))
		if err != nil {
			return err
		}
		if found && (cursor.CardID != fence.CardID || cursor.EquipmentID != fence.EquipmentID) {
			cursor = callCursor{}
			found = false
		}
		if !found {
			cursor = callCursor{Fence: fence, State: "starting", LastObservedAt: now}
		}
		cursor.Fence = fence
		cursor.LastObservedAt = now
		switch {
		case call.State == "idle" && call.VoiceCalls == 0:
			if cursor.IncomingEventID != "" && cursor.State != "idle" {
				invalidated = append(invalidated, cursor.IncomingEventID)
				if err := retireNonterminalCallEvents(tx, cursor.IncomingEventID); err != nil {
					return err
				}
				cursor.State = "idle"
				cursor.Revision++
				created, err := enqueue(tx, callEvent(cursor, call.ObservedAt.UTC(), false))
				if err != nil {
					return err
				}
				inserted = inserted || created
			}
			cursor.State, cursor.HadIdle = "idle", true
		case call.VoiceCalls == 1 && call.IncomingCalls == 1 && call.Direction == "in" && call.State == "ringing_in":
			if cursor.IncomingEventID == "" || cursor.State == "idle" {
				occurrence, err := nextOccurrence(tx, fence.EquipmentID, fence.CardID)
				if err != nil {
					return err
				}
				cursor.Occurrence = occurrence
				cursor.IncomingEventID = incomingEventID(fence.EquipmentID, fence.CardID, call.NativeIndex, occurrence)
				cursor.NativeIndex, cursor.FirstObservedAt, cursor.Revision = call.NativeIndex, call.ObservedAt.UTC(), 0
				cursor.Number = strings.TrimSpace(call.Number)
				cursor.State = "ringing_in"
				cursor.Revision++
				created, err := enqueue(tx, callEvent(cursor, call.ObservedAt.UTC(), cursor.HadIdle))
				if err != nil {
					return err
				}
				inserted = inserted || created
				cursor.LastEmittedAt = now
				validated = cursor.IncomingEventID
				if err := rebindCallEvents(tx, cursor.IncomingEventID, fence); err != nil {
					return err
				}
			} else if cursor.NativeIndex == call.NativeIndex && cursor.State == "ringing_in" {
				validated = cursor.IncomingEventID
				if err := rebindCallEvents(tx, cursor.IncomingEventID, fence); err != nil {
					return err
				}
				if cursor.Number == "" && strings.TrimSpace(call.Number) != "" {
					cursor.Number = strings.TrimSpace(call.Number)
					cursor.Revision++
					created, err := enqueue(tx, callEvent(cursor, call.ObservedAt.UTC(), false))
					if err != nil {
						return err
					}
					inserted = inserted || created
					cursor.LastEmittedAt = now
				} else if (cursor.LastEmittedAt.IsZero() || now.Sub(cursor.LastEmittedAt) >= 3*time.Second) &&
					!pendingCallEvent(tx, cursor.IncomingEventID) {
					cursor.Revision++
					created, err := enqueue(tx, callEvent(cursor, call.ObservedAt.UTC(), false))
					if err != nil {
						return err
					}
					inserted = inserted || created
					cursor.LastEmittedAt = now
				}
			} else {
				invalidated = append(invalidated, cursor.IncomingEventID)
				if err := retireNonterminalCallEvents(tx, cursor.IncomingEventID); err != nil {
					return err
				}
				cursor.State = "ambiguous"
			}
		case call.VoiceCalls == 1 && call.Direction == "in" && (call.State == "active" || call.State == "held"):
			if cursor.IncomingEventID != "" && cursor.NativeIndex == call.NativeIndex && cursor.State != "idle" {
				validated = cursor.IncomingEventID
				if err := rebindCallEvents(tx, cursor.IncomingEventID, fence); err != nil {
					return err
				}
				cursor.State = call.State
				cursor.Revision++
				created, err := enqueue(tx, callEvent(cursor, call.ObservedAt.UTC(), false))
				if err != nil {
					return err
				}
				inserted = inserted || created
			} else {
				invalidated = append(invalidated, cursor.IncomingEventID)
				if err := retireNonterminalCallEvents(tx, cursor.IncomingEventID); err != nil {
					return err
				}
				cursor.State = "ambiguous"
			}
		default:
			invalidated = append(invalidated, cursor.IncomingEventID)
			if err := retireNonterminalCallEvents(tx, cursor.IncomingEventID); err != nil {
				return err
			}
			cursor.State = "ambiguous"
		}
		wire, err := json.Marshal(cursor)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(fence.EquipmentID), wire)
	})
	if err == nil {
		for _, incomingEventID := range invalidated {
			store.unvalidateCall(incomingEventID)
		}
		if validated != "" {
			store.validateCall(validated)
		}
	}
	if err == nil && inserted {
		store.notify()
	}
	return err
}

func (store *Store) RequireIncomingCall(expected agentmodem.IncomingCallFence) error {
	if !validFence(Fence{AttachmentID: expected.AttachmentID, EquipmentID: expected.EquipmentID,
		CardID: expected.CardID, SIMSessionGeneration: expected.SIMSessionGeneration}) ||
		expected.EventID == "" || expected.NativeCallIndex < 1 || expected.CallOccurrence == 0 {
		return agentmodem.ErrIncomingCallChanged
	}
	var cursor callCursor
	err := store.db.View(func(tx *bolt.Tx) error {
		var found bool
		var err error
		cursor, found, err = decodeCursor(tx.Bucket(bucketCallCursor).Get([]byte(expected.EquipmentID)))
		if err != nil {
			return err
		}
		if !found {
			return agentmodem.ErrIncomingCallChanged
		}
		return nil
	})
	if err != nil {
		return err
	}
	if cursor.IncomingEventID != expected.EventID || cursor.AttachmentID != expected.AttachmentID ||
		cursor.EquipmentID != expected.EquipmentID || cursor.CardID != expected.CardID ||
		cursor.SIMSessionGeneration != expected.SIMSessionGeneration || cursor.NativeIndex != expected.NativeCallIndex ||
		cursor.Occurrence != expected.CallOccurrence || cursor.State != "ringing_in" ||
		time.Since(cursor.LastObservedAt) > 10*time.Second ||
		strings.TrimSpace(expected.Number) != "" && cursor.Number != "" && cursor.Number != strings.TrimSpace(expected.Number) {
		return agentmodem.ErrIncomingCallChanged
	}
	return nil
}

// ReconcileFences runs once after each Agent process has a ready topology and
// before its outbox may be sent. Durable business IDs stay unchanged; only a
// unique same-equipment/same-card event may adopt the current attachment and
// SIM session. Stale non-terminal events are retired instead of being rebound
// to another card or call.
func (store *Store) ReconcileFences(topology agentlink.TopologySnapshot) error {
	fences := sortedFences(topology, false)
	byEquipment := make(map[string][]Fence, len(fences))
	for _, fence := range fences {
		byEquipment[fence.EquipmentID] = append(byEquipment[fence.EquipmentID], fence)
	}
	err := store.db.Update(func(tx *bolt.Tx) error {
		cursors := tx.Bucket(bucketCallCursor)
		type bucketUpdate struct{ key, wire []byte }
		cursorUpdates := []bucketUpdate{}
		if err := cursors.ForEach(func(key, value []byte) error {
			cursor, found, err := decodeCursor(value)
			if err != nil || !found {
				return err
			}
			matches := matchingFences(byEquipment[cursor.EquipmentID], cursor.CardID)
			if len(matches) != 1 {
				store.unvalidateCall(cursor.IncomingEventID)
				if cursor.IncomingEventID != "" && cursor.State != "idle" && cursor.State != "detached" && cursor.State != "unavailable" {
					cursor.State = "unavailable"
					cursor.Revision++
					if _, err := enqueue(tx, callEvent(cursor, time.Now().UTC(), false)); err != nil {
						return err
					}
				}
				cursor.State, cursor.HadIdle = "detached", false
				cursor.IncomingEventID, cursor.NativeIndex, cursor.Number = "", 0, ""
				cursor.FirstObservedAt, cursor.LastEmittedAt = time.Time{}, time.Time{}
				wire, err := json.Marshal(cursor)
				if err != nil {
					return err
				}
				cursorUpdates = append(cursorUpdates, bucketUpdate{key: append([]byte(nil), key...), wire: wire})
				return nil
			}
			// A ready topology proves only card presence. Non-terminal call
			// identity is rebound later by ObserveCall after a fresh full CLCC.
			if cursor.Fence != matches[0] {
				store.unvalidateCall(cursor.IncomingEventID)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, update := range cursorUpdates {
			if err := cursors.Put(update.key, update.wire); err != nil {
				return err
			}
		}
		deletions := tx.Bucket(bucketSMSDelete)
		deletionUpdates := []bucketUpdate{}
		if err := deletions.ForEach(func(key, wire []byte) error {
			var deletion SMSDeletion
			if json.Unmarshal(wire, &deletion) != nil || !validFence(deletion.Fence) ||
				!validSMSDeletion(deletion.Indices, deletion.Fingerprint) || deletion.QueuedAt.IsZero() {
				return errors.New("stored SMS deletion is invalid")
			}
			matches := matchingFences(byEquipment[deletion.EquipmentID], deletion.CardID)
			if len(matches) != 1 || matches[0] == deletion.Fence {
				return nil
			}
			deletion.Fence = matches[0]
			encoded, err := json.Marshal(deletion)
			if err != nil {
				return err
			}
			deletionUpdates = append(deletionUpdates, bucketUpdate{key: append([]byte(nil), key...), wire: encoded})
			return nil
		}); err != nil {
			return err
		}
		for _, update := range deletionUpdates {
			if err := deletions.Put(update.key, update.wire); err != nil {
				return err
			}
		}
		outbox, ids := tx.Bucket(bucketOutbox), tx.Bucket(bucketOutboxIDs)
		var retire [][]byte
		outboxUpdates := []bucketUpdate{}
		if err := outbox.ForEach(func(key, value []byte) error {
			var event agentlink.ModemEvent
			if json.Unmarshal(value, &event) != nil || event.Validate() != nil {
				return errors.New("stored Agent modem event is invalid")
			}
			matches := matchingFences(byEquipment[event.EquipmentID], event.CardID)
			if len(matches) == 1 {
				if event.Call != nil {
					return nil
				}
				event.AttachmentID = matches[0].AttachmentID
				event.SIMSessionGeneration = matches[0].SIMSessionGeneration
				wire, err := json.Marshal(event)
				if err != nil {
					return err
				}
				outboxUpdates = append(outboxUpdates, bucketUpdate{key: append([]byte(nil), key...), wire: wire})
				return nil
			}
			if event.Kind == agentlink.ModemEventKindCall && event.Call != nil &&
				(event.Call.State == "idle" || event.Call.State == "unavailable") {
				return nil
			}
			retire = append(retire, append([]byte(nil), key...))
			return nil
		}); err != nil {
			return err
		}
		for _, update := range outboxUpdates {
			if err := outbox.Put(update.key, update.wire); err != nil {
				return err
			}
		}
		for _, key := range retire {
			var event agentlink.ModemEvent
			if err := json.Unmarshal(outbox.Get(key), &event); err != nil {
				return err
			}
			if err := outbox.Delete(key); err != nil {
				return err
			}
			if err := ids.Delete([]byte(event.EventID)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		store.MarkReady()
	}
	return err
}

func (store *Store) PendingModemEvents(_ time.Time, limit int) ([]agentlink.ModemEvent, error) {
	if !store.ready.Load() {
		return []agentlink.ModemEvent{}, nil
	}
	if limit < 1 || limit > 256 {
		return nil, errors.New("invalid Agent event outbox limit")
	}
	result := make([]agentlink.ModemEvent, 0, limit)
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketOutbox).ForEach(func(_, value []byte) error {
			if len(result) >= limit {
				return nil
			}
			var event agentlink.ModemEvent
			if json.Unmarshal(value, &event) != nil || event.Validate() != nil {
				return errors.New("stored Agent modem event is invalid")
			}
			if event.Call != nil && event.Call.State != "idle" && event.Call.State != "unavailable" &&
				!store.callValidated(event.Call.IncomingEventID) {
				return nil
			}
			result = append(result, event)
			return nil
		})
	})
	return result, err
}

func (store *Store) validateCall(incomingEventID string) {
	if incomingEventID == "" {
		return
	}
	store.validatedMu.Lock()
	store.validatedCalls[incomingEventID] = struct{}{}
	store.validatedMu.Unlock()
}

func (store *Store) unvalidateCall(incomingEventID string) {
	if incomingEventID == "" {
		return
	}
	store.validatedMu.Lock()
	delete(store.validatedCalls, incomingEventID)
	store.validatedMu.Unlock()
}

func (store *Store) callValidated(incomingEventID string) bool {
	store.validatedMu.RLock()
	_, exists := store.validatedCalls[incomingEventID]
	store.validatedMu.RUnlock()
	return exists
}

func (store *Store) AckModemEvent(eventID string) error {
	if strings.TrimSpace(eventID) == "" {
		return errors.New("Agent event identity is required")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		ids := tx.Bucket(bucketOutboxIDs)
		key := append([]byte(nil), ids.Get([]byte(eventID))...)
		if key == nil {
			return nil
		}
		var event agentlink.ModemEvent
		if json.Unmarshal(tx.Bucket(bucketOutbox).Get(key), &event) != nil || event.Validate() != nil {
			return errors.New("stored Agent modem event is invalid")
		}
		if event.SMS != nil {
			if err := putSMSDeletion(tx, Fence{AttachmentID: event.AttachmentID, EquipmentID: event.EquipmentID,
				CardID: event.CardID, SIMSessionGeneration: event.SIMSessionGeneration},
				event.SMS.Fingerprint, event.SMS.StorageIndices, time.Now().UTC()); err != nil {
				return err
			}
		}
		return removeOutboxTx(tx, eventID, key)
	})
}

func (store *Store) RejectModemEvent(eventID, code string) error {
	if strings.TrimSpace(code) == "" {
		return errors.New("Agent event rejection code is required")
	}
	return store.removeOutbox(eventID)
}

func (store *Store) removeOutbox(eventID string) error {
	if strings.TrimSpace(eventID) == "" {
		return errors.New("Agent event identity is required")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		ids := tx.Bucket(bucketOutboxIDs)
		key := append([]byte(nil), ids.Get([]byte(eventID))...)
		if key == nil {
			return nil
		}
		return removeOutboxTx(tx, eventID, key)
	})
}

func removeOutboxTx(tx *bolt.Tx, eventID string, key []byte) error {
	if err := tx.Bucket(bucketOutbox).Delete(key); err != nil {
		return err
	}
	return tx.Bucket(bucketOutboxIDs).Delete([]byte(eventID))
}

func (store *Store) PendingSMSDeletion(fences []Fence) (SMSDeletion, bool, error) {
	var result SMSDeletion
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSMSDelete).ForEach(func(_, wire []byte) error {
			if found {
				return nil
			}
			var deletion SMSDeletion
			if json.Unmarshal(wire, &deletion) != nil || !validFence(deletion.Fence) ||
				!validSMSDeletion(deletion.Indices, deletion.Fingerprint) || deletion.QueuedAt.IsZero() {
				return errors.New("stored SMS deletion is invalid")
			}
			for _, fence := range fences {
				if fence == deletion.Fence {
					result, found = deletion, true
					break
				}
			}
			return nil
		})
	})
	return result, found, err
}

func (store *Store) CompleteSMSDeletion(cardID, fingerprint string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSMSDelete).Delete(smsDeletionKey(cardID, fingerprint))
	})
}

func (store *Store) ModemEventWake() <-chan struct{} { return store.wake }

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.closeOnce.Do(func() { store.closeErr = store.db.Close() })
	return store.closeErr
}

func (store *Store) notify() {
	select {
	case store.wake <- struct{}{}:
	default:
	}
}

func enqueue(tx *bolt.Tx, event agentlink.ModemEvent) (bool, error) {
	if err := event.Validate(); err != nil {
		return false, err
	}
	ids := tx.Bucket(bucketOutboxIDs)
	encoded, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	if key := ids.Get([]byte(event.EventID)); key != nil {
		if !bytes.Equal(tx.Bucket(bucketOutbox).Get(key), encoded) {
			return false, errors.New("Agent event identity conflict")
		}
		return false, nil
	}
	sequence, err := tx.Bucket(bucketOutbox).NextSequence()
	if err != nil {
		return false, err
	}
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], sequence)
	if err := tx.Bucket(bucketOutbox).Put(key[:], encoded); err != nil {
		return false, err
	}
	if err := ids.Put([]byte(event.EventID), key[:]); err != nil {
		return false, err
	}
	return true, nil
}

func pendingCallEvent(tx *bolt.Tx, incomingEventID string) bool {
	found := false
	_ = tx.Bucket(bucketOutbox).ForEach(func(_, wire []byte) error {
		var event agentlink.ModemEvent
		if json.Unmarshal(wire, &event) == nil && event.Call != nil && event.Call.IncomingEventID == incomingEventID {
			found = true
		}
		return nil
	})
	return found
}

func rebindCallEvents(tx *bolt.Tx, incomingEventID string, fence Fence) error {
	if incomingEventID == "" || !validFence(fence) {
		return nil
	}
	bucket := tx.Bucket(bucketOutbox)
	type update struct{ key, wire []byte }
	updates := []update{}
	if err := bucket.ForEach(func(key, wire []byte) error {
		var event agentlink.ModemEvent
		if json.Unmarshal(wire, &event) != nil || event.Validate() != nil {
			return errors.New("stored Agent modem event is invalid")
		}
		if event.Call == nil || event.Call.IncomingEventID != incomingEventID ||
			event.Call.State == "idle" || event.Call.State == "unavailable" {
			return nil
		}
		event.AttachmentID, event.EquipmentID, event.CardID = fence.AttachmentID, fence.EquipmentID, fence.CardID
		event.SIMSessionGeneration = fence.SIMSessionGeneration
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		updates = append(updates, update{key: append([]byte(nil), key...), wire: encoded})
		return nil
	}); err != nil {
		return err
	}
	for _, item := range updates {
		if err := bucket.Put(item.key, item.wire); err != nil {
			return err
		}
	}
	return nil
}

func retireNonterminalCallEvents(tx *bolt.Tx, incomingEventID string) error {
	if incomingEventID == "" {
		return nil
	}
	outbox, ids := tx.Bucket(bucketOutbox), tx.Bucket(bucketOutboxIDs)
	type retired struct {
		key     []byte
		eventID string
	}
	retire := []retired{}
	if err := outbox.ForEach(func(key, wire []byte) error {
		var event agentlink.ModemEvent
		if json.Unmarshal(wire, &event) != nil || event.Validate() != nil {
			return errors.New("stored Agent modem event is invalid")
		}
		if event.Call != nil && event.Call.IncomingEventID == incomingEventID &&
			event.Call.State != "idle" && event.Call.State != "unavailable" {
			retire = append(retire, retired{key: append([]byte(nil), key...), eventID: event.EventID})
		}
		return nil
	}); err != nil {
		return err
	}
	for _, item := range retire {
		if err := outbox.Delete(item.key); err != nil {
			return err
		}
		if err := ids.Delete([]byte(item.eventID)); err != nil {
			return err
		}
	}
	return nil
}

func smsEvent(fence Fence, message agentmodem.SMSMessage, now time.Time) agentlink.ModemEvent {
	observed := message.ObservedAt.UTC()
	if observed.IsZero() {
		observed = now
	}
	identity := sha256.Sum256([]byte(fence.CardID + "\x00" + strings.ToLower(message.Fingerprint)))
	return agentlink.ModemEvent{
		SchemaVersion: agentlink.ModemEventSchemaVersion,
		EventID:       "cellular-sms-event-" + hex.EncodeToString(identity[:]),
		Kind:          agentlink.ModemEventKindSMS, AttachmentID: fence.AttachmentID, EquipmentID: fence.EquipmentID,
		CardID: fence.CardID, SIMSessionGeneration: fence.SIMSessionGeneration, ObservedAt: observed,
		SMS: &agentlink.ModemEventSMS{Index: message.Index, StorageIndices: append([]int(nil), message.Indices...),
			Fingerprint: message.Fingerprint, State: message.State,
			Direction: message.Direction, Peer: message.Peer, Body: message.Body,
			Reference: message.Reference, Delivery: message.Delivery},
	}
}

func callEvent(cursor callCursor, observed time.Time, notify bool) agentlink.ModemEvent {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", cursor.IncomingEventID, cursor.Revision, cursor.State)))
	return agentlink.ModemEvent{
		SchemaVersion: agentlink.ModemEventSchemaVersion,
		EventID:       "cellular-call-event-" + hex.EncodeToString(digest[:16]),
		Kind:          agentlink.ModemEventKindCall, AttachmentID: cursor.AttachmentID,
		EquipmentID: cursor.EquipmentID, CardID: cursor.CardID,
		SIMSessionGeneration: cursor.SIMSessionGeneration, ObservedAt: observed,
		Call: &agentlink.ModemEventCall{IncomingEventID: cursor.IncomingEventID,
			Occurrence: cursor.Occurrence, Revision: cursor.Revision, NativeIndex: cursor.NativeIndex, State: cursor.State,
			Direction: "in", Number: cursor.Number, FirstObservedAt: cursor.FirstObservedAt, Notify: notify},
	}
}

func nextOccurrence(tx *bolt.Tx, equipmentID, cardID string) (uint64, error) {
	key := []byte("call-occurrence\x00" + equipmentID + "\x00" + cardID)
	meta := tx.Bucket(bucketMeta)
	current := uint64(0)
	if wire := meta.Get(key); wire != nil {
		if len(wire) != 8 {
			return 0, errors.New("stored call occurrence is invalid")
		}
		current = binary.BigEndian.Uint64(wire)
	}
	current++
	var wire [8]byte
	binary.BigEndian.PutUint64(wire[:], current)
	return current, meta.Put(key, wire[:])
}

func incomingEventID(equipmentID, cardID string, nativeIndex int, occurrence uint64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", equipmentID, cardID, nativeIndex, occurrence)))
	return "cellular-incoming-" + hex.EncodeToString(digest[:16])
}

func smsSeenKey(cardID, fingerprint string) []byte {
	return []byte(cardID + "\x00" + strings.ToLower(fingerprint))
}

func putSMSDeletion(tx *bolt.Tx, fence Fence, fingerprint string, indices []int, queuedAt time.Time) error {
	indices = normalizedIndices(indices)
	if !validFence(fence) || !validSMSDeletion(indices, fingerprint) || queuedAt.IsZero() {
		return errors.New("invalid SMS deletion")
	}
	deletion := SMSDeletion{Fence: fence, Fingerprint: strings.ToLower(fingerprint),
		Indices: indices, QueuedAt: queuedAt.UTC()}
	wire, err := json.Marshal(deletion)
	if err != nil {
		return err
	}
	bucket := tx.Bucket(bucketSMSDelete)
	key := smsDeletionKey(fence.CardID, fingerprint)
	if prior := bucket.Get(key); prior != nil {
		var existing SMSDeletion
		if json.Unmarshal(prior, &existing) != nil || existing.Fingerprint != deletion.Fingerprint ||
			!sameIndices(existing.Indices, deletion.Indices) || existing.CardID != deletion.CardID ||
			existing.EquipmentID != deletion.EquipmentID {
			return errors.New("SMS deletion identity conflict")
		}
		return nil
	}
	return bucket.Put(key, wire)
}

func smsDeletionKey(cardID, fingerprint string) []byte {
	return []byte(cardID + "\x00" + strings.ToLower(fingerprint))
}

func validSMSDeletion(indices []int, fingerprint string) bool {
	if len(indices) == 0 || len(indices) > 7 || len(fingerprint) != sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return false
	}
	for index, value := range indices {
		if value < 1 || index > 0 && value <= indices[index-1] {
			return false
		}
	}
	return true
}

func normalizedIndices(input []int) []int {
	result := append([]int(nil), input...)
	sort.Ints(result)
	for index, value := range result {
		if value < 1 || index > 0 && value == result[index-1] {
			return nil
		}
	}
	return result
}

func sameIndices(left, right []int) bool {
	left, right = normalizedIndices(left), normalizedIndices(right)
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func decodeCursor(value []byte) (callCursor, bool, error) {
	if value == nil {
		return callCursor{}, false, nil
	}
	var cursor callCursor
	if err := json.Unmarshal(value, &cursor); err != nil {
		return callCursor{}, false, err
	}
	return cursor, true, nil
}

func validFence(fence Fence) bool {
	return strings.TrimSpace(fence.AttachmentID) != "" && strings.TrimSpace(fence.EquipmentID) != "" &&
		strings.TrimSpace(fence.CardID) != "" && strings.TrimSpace(fence.SIMSessionGeneration) != ""
}

func displayableSMS(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	replacements := 0
	characters := 0
	for _, character := range value {
		characters++
		if character == '\ufffd' {
			replacements++
		}
		if character == '\t' || character == '\n' || character == '\r' || character == '\f' {
			continue
		}
		if unicode.IsControl(character) || character >= 0xd800 && character <= 0xdfff {
			return false
		}
	}
	return replacements < 2 || replacements*10 < characters
}

func sortedFences(topology agentlink.TopologySnapshot, callOnly bool) []Fence {
	result := make([]Fence, 0, len(topology.Modems))
	for _, modem := range topology.Modems {
		if modem.Condition != "ready" || modem.SIM.State != "ready" || modem.SIM.ICCID == "" ||
			modem.SIM.SessionGeneration == "" || modem.AT.State != "ready" ||
			callOnly && !modem.AT.CallSignalling {
			continue
		}
		result = append(result, Fence{AttachmentID: modem.AttachmentID, EquipmentID: modem.EquipmentID,
			CardID: modem.SIM.ICCID, SIMSessionGeneration: modem.SIM.SessionGeneration})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].EquipmentID < result[right].EquipmentID })
	return result
}

func matchingFences(input []Fence, cardID string) []Fence {
	result := make([]Fence, 0, 1)
	for _, fence := range input {
		if fence.CardID == cardID {
			result = append(result, fence)
		}
	}
	return result
}
