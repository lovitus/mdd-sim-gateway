package agentsms

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

const schemaVersion = 1

var (
	bucketMetadata = []byte("metadata")
	bucketRecords  = []byte("operations")
	keySchema      = []byte("schema_version")
	ErrConflict    = errors.New("SMS operation identity conflict")
)

type Record struct {
	SchemaVersion int       `json:"schema_version"`
	OperationID   string    `json:"operation_id"`
	AttachmentID  string    `json:"attachment_id"`
	EquipmentID   string    `json:"equipment_id"`
	CardID        string    `json:"card_id"`
	Recipient     string    `json:"recipient"`
	BodySHA256    string    `json:"body_sha256"`
	State         string    `json:"state"`
	References    []int     `json:"references,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Store struct{ db *bolt.DB }

func Open(path string, timeout time.Duration) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" || timeout <= 0 {
		return nil, errors.New("invalid SMS operation store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

func (store *Store) initialize() error {
	return store.db.Update(func(tx *bolt.Tx) error {
		metadata, err := tx.CreateBucketIfNotExists(bucketMetadata)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketRecords); err != nil {
			return err
		}
		var wire [8]byte
		binary.BigEndian.PutUint64(wire[:], schemaVersion)
		stored := metadata.Get(keySchema)
		if stored == nil {
			return metadata.Put(keySchema, wire[:])
		}
		if !bytes.Equal(stored, wire[:]) {
			return errors.New("unsupported SMS operation store schema")
		}
		return nil
	})
}

func (store *Store) Begin(record Record) (Record, bool, error) {
	record.SchemaVersion = schemaVersion
	record.State = "prepared"
	record.References = nil
	record.CreatedAt = record.CreatedAt.UTC()
	var result Record
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketRecords)
		key := []byte(record.OperationID)
		if prior := bucket.Get(key); prior != nil {
			if json.Unmarshal(prior, &result) != nil {
				return errors.New("invalid persisted SMS operation")
			}
			if !sameRequest(result, record) {
				return ErrConflict
			}
			return nil
		}
		wire, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := bucket.Put(key, wire); err != nil {
			return err
		}
		result, created = record, true
		return nil
	})
	return result, created, err
}

func (store *Store) Mark(operationID, state string, references []int) (Record, error) {
	var result Record
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketRecords)
		wire := bucket.Get([]byte(operationID))
		if wire == nil || json.Unmarshal(wire, &result) != nil {
			return errors.New("SMS operation not found")
		}
		result.State = state
		result.References = append([]int(nil), references...)
		updated, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(operationID), updated)
	})
	return result, err
}

func (store *Store) Delete(operationID string) error {
	return store.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketRecords).Delete([]byte(operationID)) })
}

func (store *Store) Close() error { return store.db.Close() }

func sameRequest(left, right Record) bool {
	return left.SchemaVersion == right.SchemaVersion && left.OperationID == right.OperationID &&
		left.AttachmentID == right.AttachmentID && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID && left.Recipient == right.Recipient && left.BodySHA256 == right.BodySHA256
}
