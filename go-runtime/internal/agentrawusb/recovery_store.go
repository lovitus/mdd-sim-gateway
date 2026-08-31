package agentrawusb

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const recoveryStoreSchema uint64 = 1

var (
	recoveryMetadataBucket = []byte("metadata")
	recoveryRecordsBucket  = []byte("raw_modem_handoffs")
	recoverySchemaKey      = []byte("schema_version")
	ErrRecoveryConflict    = errors.New("another raw modem handoff owns this equipment")
	ErrRecoveryNotFound    = errors.New("raw modem handoff was not found")
	ErrRecoveryMismatch    = errors.New("raw modem handoff identity does not match")
)

// RecoveryRecord is written before the source Agent releases its local AT
// owner. It is deliberately independent of Core, WSS and the raw feature flag:
// after any crash the source must reacquire this exact modem/card and obtain a
// terminal call-state proof before the record can be removed.
type RecoveryRecord struct {
	SchemaVersion           uint64    `json:"schema_version"`
	SourceAgentID           string    `json:"source_agent_id"`
	SourceProcessGeneration string    `json:"source_process_generation"`
	AttachmentID            string    `json:"attachment_id"`
	SessionGeneration       string    `json:"session_generation"`
	EquipmentID             string    `json:"equipment_id"`
	CardID                  string    `json:"card_id"`
	USBSessionID            string    `json:"usb_session_id"`
	ArmedAt                 time.Time `json:"armed_at"`
}

type RecoveryStore struct {
	db        *bolt.DB
	closeOnce sync.Once
	closeErr  error
}

func OpenRecoveryStore(path string, timeout time.Duration) (*RecoveryStore, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid raw modem recovery store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create raw modem recovery directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("protect raw modem recovery directory: %w", err)
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, fmt.Errorf("stat raw modem recovery store: %w", statErr)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("open raw modem recovery store: %w", err)
	}
	store := &RecoveryStore{db: db}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if created {
		if err := syncRecoveryParent(filepath.Dir(path)); err != nil {
			return nil, errors.Join(err, db.Close())
		}
	}
	return store, nil
}

func (store *RecoveryStore) initialize() error {
	return store.db.Update(func(tx *bolt.Tx) error {
		metadata, err := tx.CreateBucketIfNotExists(recoveryMetadataBucket)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(recoveryRecordsBucket); err != nil {
			return err
		}
		stored := metadata.Get(recoverySchemaKey)
		if stored == nil {
			encoded := make([]byte, 8)
			binary.BigEndian.PutUint64(encoded, recoveryStoreSchema)
			return metadata.Put(recoverySchemaKey, encoded)
		}
		if len(stored) != 8 || binary.BigEndian.Uint64(stored) != recoveryStoreSchema {
			return errors.New("unsupported raw modem recovery store schema")
		}
		return nil
	})
}

func (store *RecoveryStore) Arm(input RecoveryRecord) (RecoveryRecord, bool, error) {
	record := input
	if record.SchemaVersion == 0 {
		record.SchemaVersion = recoveryStoreSchema
	}
	record.ArmedAt = record.ArmedAt.UTC()
	if err := validateRecoveryRecord(record); err != nil {
		return RecoveryRecord{}, false, err
	}
	result, created := RecoveryRecord{}, false
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recoveryRecordsBucket)
		if payload := bucket.Get([]byte(record.EquipmentID)); payload != nil {
			current, err := decodeRecoveryRecord(payload)
			if err != nil {
				return err
			}
			if sameRecoveryIdentity(current, record) {
				result = current
				return nil
			}
			return ErrRecoveryConflict
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

func (store *RecoveryStore) Records() ([]RecoveryRecord, error) {
	result := []RecoveryRecord{}
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(recoveryRecordsBucket).ForEach(func(_, payload []byte) error {
			record, err := decodeRecoveryRecord(payload)
			if err != nil {
				return err
			}
			result = append(result, record)
			return nil
		})
	})
	sort.Slice(result, func(left, right int) bool { return result[left].EquipmentID < result[right].EquipmentID })
	return result, err
}

func (store *RecoveryStore) ClearExpected(expected RecoveryRecord) error {
	if err := validateRecoveryRecord(expected); err != nil {
		return err
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recoveryRecordsBucket)
		payload := bucket.Get([]byte(expected.EquipmentID))
		if payload == nil {
			return ErrRecoveryNotFound
		}
		current, err := decodeRecoveryRecord(payload)
		if err != nil {
			return err
		}
		if !sameRecoveryIdentity(current, expected) {
			return ErrRecoveryMismatch
		}
		return bucket.Delete([]byte(expected.EquipmentID))
	})
}

func (store *RecoveryStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.closeOnce.Do(func() { store.closeErr = store.db.Close() })
	return store.closeErr
}

func decodeRecoveryRecord(payload []byte) (RecoveryRecord, error) {
	if payload == nil {
		return RecoveryRecord{}, ErrRecoveryNotFound
	}
	var record RecoveryRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return RecoveryRecord{}, err
	}
	if err := validateRecoveryRecord(record); err != nil {
		return RecoveryRecord{}, err
	}
	return record, nil
}

func validateRecoveryRecord(record RecoveryRecord) error {
	values := []string{record.SourceAgentID, record.SourceProcessGeneration, record.AttachmentID,
		record.SessionGeneration, record.EquipmentID, record.CardID, record.USBSessionID}
	if record.SchemaVersion != recoveryStoreSchema || record.ArmedAt.IsZero() {
		return errors.New("invalid raw modem recovery record")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 128 {
			return errors.New("invalid raw modem recovery record")
		}
	}
	return nil
}

func sameRecoveryIdentity(left, right RecoveryRecord) bool {
	return left.SchemaVersion == right.SchemaVersion && left.SourceAgentID == right.SourceAgentID &&
		left.SourceProcessGeneration == right.SourceProcessGeneration && left.AttachmentID == right.AttachmentID &&
		left.SessionGeneration == right.SessionGeneration && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID && left.USBSessionID == right.USBSessionID
}
