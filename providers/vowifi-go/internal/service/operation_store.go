// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	bolt "go.etcd.io/bbolt"
)

var operationBucket = []byte("operations-v1")
var messageOutboxBucket = []byte("message-outbox-v1")
var maintenanceBucket = []byte("maintenance-v1")
var drainLeaseKey = []byte("apply-drain-lease")

type OperationRecord struct {
	Kind    string                    `json:"kind"`
	Done    bool                      `json:"done"`
	Result  vowifiipc.OperationResult `json:"result"`
	Failure *vowifiipc.OperationError `json:"failure,omitempty"`
}

type OperationStore interface {
	Lookup(generation, operationID string) (OperationRecord, bool, error)
	Reserve(generation, operationID, kind string) error
	Complete(generation, operationID string, result vowifiipc.OperationResult) error
	CompleteFailure(generation, operationID string, failure *vowifiipc.OperationError) error
	MaintenanceLease() (string, error)
	BeginMaintenance(string) error
	EndMaintenance(string) error
}

type MessageOutbox interface {
	AdoptMessages(lineID, providerID, generation string) error
	EnqueueMessage(providermessages.Event) error
	PendingMessages(int) ([]providermessages.Event, error)
	DeleteMessage(providermessages.Event) error
}

type MemoryOperationStore struct {
	mu         sync.Mutex
	records    map[string]OperationRecord
	messages   map[string]providermessages.Event
	drainLease string
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{records: make(map[string]OperationRecord), messages: make(map[string]providermessages.Event)}
}

func (store *MemoryOperationStore) Lookup(generation, operationID string) (OperationRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[operationKey(generation, operationID)]
	return record, found, nil
}
func (store *MemoryOperationStore) Reserve(generation, operationID, kind string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := operationKey(generation, operationID)
	if _, found := store.records[key]; found {
		return errors.New("operation already reserved")
	}
	store.records[key] = OperationRecord{Kind: kind}
	return nil
}
func (store *MemoryOperationStore) Complete(generation, operationID string, result vowifiipc.OperationResult) error {
	return store.finish(generation, operationID, result, nil)
}
func (store *MemoryOperationStore) CompleteFailure(generation, operationID string, failure *vowifiipc.OperationError) error {
	return store.finish(generation, operationID, vowifiipc.OperationResult{}, failure)
}
func (store *MemoryOperationStore) finish(generation, operationID string, result vowifiipc.OperationResult, failure *vowifiipc.OperationError) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := operationKey(generation, operationID)
	record, found := store.records[key]
	if !found || record.Done {
		return errors.New("operation is not pending")
	}
	record.Done, record.Result, record.Failure = true, result, failure
	store.records[key] = record
	return nil
}

func (store *MemoryOperationStore) MaintenanceLease() (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.drainLease, nil
}

func (store *MemoryOperationStore) BeginMaintenance(leaseID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.drainLease != "" && store.drainLease != leaseID {
		return errors.New("another maintenance lease is active")
	}
	store.drainLease = leaseID
	return nil
}

func (store *MemoryOperationStore) EndMaintenance(leaseID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.drainLease != leaseID {
		return errors.New("maintenance lease does not match")
	}
	store.drainLease = ""
	return nil
}

func (store *MemoryOperationStore) EnqueueMessage(event providermessages.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := messageKey(event)
	if prior, found := store.messages[key]; found {
		priorWire, _ := json.Marshal(prior)
		wire, _ := json.Marshal(event)
		if string(priorWire) != string(wire) {
			return errors.New("message outbox event ID conflict")
		}
		return nil
	}
	store.messages[key] = event
	return nil
}

func (store *MemoryOperationStore) AdoptMessages(lineID, providerID, generation string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	next := make(map[string]providermessages.Event, len(store.messages))
	for _, event := range store.messages {
		if event.LineID == lineID && event.ProviderID == providerID {
			event.ProcessGeneration = generation
		}
		next[messageKey(event)] = event
	}
	store.messages = next
	return nil
}

func (store *MemoryOperationStore) PendingMessages(limit int) ([]providermessages.Event, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if limit <= 0 {
		return nil, errors.New("invalid message outbox limit")
	}
	result := make([]providermessages.Event, 0, min(limit, len(store.messages)))
	for _, event := range store.messages {
		result = append(result, event)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (store *MemoryOperationStore) DeleteMessage(event providermessages.Event) error {
	store.mu.Lock()
	delete(store.messages, messageKey(event))
	store.mu.Unlock()
	return nil
}

type BoltOperationStore struct{ db *bolt.DB }

func OpenBoltOperationStore(path string) (*BoltOperationStore, error) {
	if path == "" {
		return nil, errors.New("operation store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, statErr
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(operationBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(messageOutboxBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(maintenanceBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	if created {
		if err := syncParentDirectory(filepath.Dir(path)); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &BoltOperationStore{db: db}, nil
}

func (store *BoltOperationStore) Close() error { return store.db.Close() }
func (store *BoltOperationStore) Lookup(generation, operationID string) (OperationRecord, bool, error) {
	var record OperationRecord
	var found bool
	err := store.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(operationBucket).Get([]byte(operationKey(generation, operationID)))
		if value == nil {
			return nil
		}
		found = true
		return json.Unmarshal(value, &record)
	})
	return record, found, err
}
func (store *BoltOperationStore) Reserve(generation, operationID, kind string) error {
	return store.writeNew(operationKey(generation, operationID), OperationRecord{Kind: kind})
}
func (store *BoltOperationStore) Complete(generation, operationID string, result vowifiipc.OperationResult) error {
	return store.finish(operationKey(generation, operationID), result, nil)
}
func (store *BoltOperationStore) CompleteFailure(generation, operationID string, failure *vowifiipc.OperationError) error {
	return store.finish(operationKey(generation, operationID), vowifiipc.OperationResult{}, failure)
}
func (store *BoltOperationStore) writeNew(key string, record OperationRecord) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationBucket)
		if bucket.Get([]byte(key)) != nil {
			return errors.New("operation already reserved")
		}
		value, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(key), value)
	})
}
func (store *BoltOperationStore) finish(key string, result vowifiipc.OperationResult, failure *vowifiipc.OperationError) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationBucket)
		value := bucket.Get([]byte(key))
		if value == nil {
			return errors.New("operation is not pending")
		}
		var record OperationRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if record.Done {
			return errors.New("operation is already complete")
		}
		record.Done, record.Result, record.Failure = true, result, failure
		wire, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(key), wire)
	})
}

func (store *BoltOperationStore) MaintenanceLease() (string, error) {
	var lease string
	err := store.db.View(func(tx *bolt.Tx) error {
		lease = string(tx.Bucket(maintenanceBucket).Get(drainLeaseKey))
		return nil
	})
	return lease, err
}

func (store *BoltOperationStore) BeginMaintenance(leaseID string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(maintenanceBucket)
		current := string(bucket.Get(drainLeaseKey))
		if current != "" && current != leaseID {
			return errors.New("another maintenance lease is active")
		}
		return bucket.Put(drainLeaseKey, []byte(leaseID))
	})
}

func (store *BoltOperationStore) EndMaintenance(leaseID string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(maintenanceBucket)
		if string(bucket.Get(drainLeaseKey)) != leaseID {
			return errors.New("maintenance lease does not match")
		}
		return bucket.Delete(drainLeaseKey)
	})
}

func (store *BoltOperationStore) EnqueueMessage(event providermessages.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	wire, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(messageOutboxBucket)
		key := []byte(messageKey(event))
		if prior := bucket.Get(key); prior != nil {
			if string(prior) != string(wire) {
				return errors.New("message outbox event ID conflict")
			}
			return nil
		}
		return bucket.Put(key, wire)
	})
}

func (store *BoltOperationStore) AdoptMessages(lineID, providerID, generation string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(messageOutboxBucket)
		var adopted []providermessages.Event
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var event providermessages.Event
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			if event.LineID != lineID || event.ProviderID != providerID || event.ProcessGeneration == generation {
				continue
			}
			event.ProcessGeneration = generation
			adopted = append(adopted, event)
			if err := cursor.Delete(); err != nil {
				return err
			}
		}
		for _, event := range adopted {
			wire, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if err := bucket.Put([]byte(messageKey(event)), wire); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *BoltOperationStore) PendingMessages(limit int) ([]providermessages.Event, error) {
	if limit <= 0 {
		return nil, errors.New("invalid message outbox limit")
	}
	result := make([]providermessages.Event, 0, limit)
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(messageOutboxBucket).Cursor()
		for key, value := cursor.First(); key != nil && len(result) < limit; key, value = cursor.Next() {
			var event providermessages.Event
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			result = append(result, event)
		}
		return nil
	})
	return result, err
}

func (store *BoltOperationStore) DeleteMessage(event providermessages.Event) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(messageOutboxBucket).Delete([]byte(messageKey(event)))
	})
}

func messageKey(event providermessages.Event) string {
	return event.LineID + "\x00" + event.ProviderID + "\x00" + event.ProcessGeneration + "\x00" + event.EventID
}

func operationKey(generation, operationID string) string { return generation + "\x00" + operationID }
