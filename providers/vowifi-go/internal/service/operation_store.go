// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	bolt "go.etcd.io/bbolt"
)

var operationBucket = []byte("lifecycle-operations-v1")

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
}

type MemoryOperationStore struct {
	mu      sync.Mutex
	records map[string]OperationRecord
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{records: make(map[string]OperationRecord)}
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
	if err := db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucketIfNotExists(operationBucket); return err }); err != nil {
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

func operationKey(generation, operationID string) string { return generation + "\x00" + operationID }
