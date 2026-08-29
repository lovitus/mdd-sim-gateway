// Package agentcall owns the durable safety lease written before any modem
// command that can start billing. It intentionally contains no network or UI
// policy; the local Agent remains able to terminate a call after Core exits.
package agentcall

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const schemaVersion uint64 = 1

var (
	ErrLeaseConflict = errors.New("another paid-call lease owns this modem")
	ErrLeaseNotFound = errors.New("paid-call lease was not found")
	ErrLeaseMismatch = errors.New("paid-call lease identity does not match")
	ErrLeaseExpired  = errors.New("paid-call lease has expired")
)

var (
	bucketMeta   = []byte("metadata")
	bucketLeases = []byte("paid_call_leases")
	keySchema    = []byte("schema_version")
)

type Record struct {
	SchemaVersion uint64    `json:"schema_version"`
	LeaseID       string    `json:"lease_id"`
	OperationID   string    `json:"operation_id"`
	AttachmentID  string    `json:"attachment_id"`
	EquipmentID   string    `json:"equipment_id"`
	CardID        string    `json:"card_id"`
	Direction     string    `json:"direction"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type Store struct {
	db        *bolt.DB
	closeOnce sync.Once
	closeErr  error
}

func Open(path string, timeout time.Duration) (*Store, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid paid-call store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create paid-call state directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("protect paid-call state directory: %w", err)
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, fmt.Errorf("stat paid-call store: %w", statErr)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("open paid-call store: %w", err)
	}
	store := &Store{db: db}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if created {
		if err := syncParentDirectory(filepath.Dir(path)); err != nil {
			return nil, errors.Join(err, db.Close())
		}
	}
	return store, nil
}

func (store *Store) initialize() error {
	return store.db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(bucketMeta)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketLeases); err != nil {
			return err
		}
		stored := meta.Get(keySchema)
		if stored == nil {
			encoded := make([]byte, 8)
			binary.BigEndian.PutUint64(encoded, schemaVersion)
			return meta.Put(keySchema, encoded)
		}
		if len(stored) != 8 || binary.BigEndian.Uint64(stored) != schemaVersion {
			return errors.New("unsupported paid-call store schema")
		}
		return nil
	})
}

func (store *Store) Begin(record Record) (Record, bool, error) {
	if err := validateRecord(record); err != nil {
		return Record{}, false, err
	}
	var result Record
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketLeases)
		current, err := decodeRecord(bucket.Get([]byte(record.EquipmentID)))
		if err != nil && !errors.Is(err, ErrLeaseNotFound) {
			return err
		}
		if err == nil {
			if sameInvocation(current, record) {
				result = current
				return nil
			}
			return ErrLeaseConflict
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(record.EquipmentID), payload); err != nil {
			return err
		}
		result, created = record, true
		return nil
	})
	return result, created, err
}

func (store *Store) Renew(attachmentID, equipmentID, cardID, leaseID string, now, expiresAt time.Time) (Record, error) {
	var result Record
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketLeases)
		current, err := decodeRecord(bucket.Get([]byte(equipmentID)))
		if err != nil {
			return err
		}
		if current.AttachmentID != attachmentID || current.CardID != cardID || current.LeaseID != leaseID {
			return ErrLeaseMismatch
		}
		if !now.Before(current.ExpiresAt) {
			return ErrLeaseExpired
		}
		current.ExpiresAt = expiresAt.UTC()
		if err := validateRecord(current); err != nil {
			return err
		}
		payload, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(equipmentID), payload); err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}

func (store *Store) ClearTarget(attachmentID, equipmentID, cardID string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketLeases)
		current, err := decodeRecord(bucket.Get([]byte(equipmentID)))
		if errors.Is(err, ErrLeaseNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.AttachmentID != attachmentID || current.CardID != cardID {
			return ErrLeaseMismatch
		}
		return bucket.Delete([]byte(equipmentID))
	})
}

func (store *Store) Records() ([]Record, error) {
	result := []Record{}
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketLeases).ForEach(func(_, value []byte) error {
			record, err := decodeRecord(value)
			if err != nil {
				return err
			}
			result = append(result, record)
			return nil
		})
	})
	return result, err
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.closeOnce.Do(func() { store.closeErr = store.db.Close() })
	return store.closeErr
}

func decodeRecord(payload []byte) (Record, error) {
	if payload == nil {
		return Record{}, ErrLeaseNotFound
	}
	var record Record
	if err := json.Unmarshal(payload, &record); err != nil {
		return Record{}, err
	}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func validateRecord(record Record) error {
	if record.SchemaVersion != schemaVersion || record.LeaseID == "" || record.OperationID == "" ||
		record.AttachmentID == "" || record.EquipmentID == "" || record.CardID == "" ||
		len(record.LeaseID) > 128 || len(record.OperationID) > 128 || len(record.AttachmentID) > 128 ||
		len(record.EquipmentID) > 128 || len(record.CardID) > 64 ||
		(record.Direction != "out" && record.Direction != "in") || record.ExpiresAt.IsZero() {
		return errors.New("invalid paid-call lease record")
	}
	return nil
}

func sameInvocation(left, right Record) bool {
	return left.LeaseID == right.LeaseID && left.OperationID == right.OperationID &&
		left.AttachmentID == right.AttachmentID && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID && left.Direction == right.Direction
}
