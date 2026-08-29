package agentpin

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

const schemaVersion uint64 = 1

var (
	bucketMetadata = []byte("metadata")
	bucketAttempts = []byte("attempts")
	keySchema      = []byte("schema_version")
)

type Record struct {
	SchemaVersion uint64    `json:"schema_version"`
	CardID        string    `json:"card_id"`
	PINRevision   string    `json:"pin_revision"`
	AttemptedAt   time.Time `json:"attempted_at"`
}

type Store struct{ db *bolt.DB }

func Open(path string, timeout time.Duration) (*Store, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid SIM PIN attempt store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
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
		if _, err := tx.CreateBucketIfNotExists(bucketAttempts); err != nil {
			return err
		}
		var wire [8]byte
		binary.BigEndian.PutUint64(wire[:], schemaVersion)
		stored := metadata.Get(keySchema)
		if stored == nil {
			return metadata.Put(keySchema, wire[:])
		}
		if !bytes.Equal(stored, wire[:]) {
			return errors.New("unsupported SIM PIN attempt store schema")
		}
		return nil
	})
}

func (store *Store) Prepare(record Record) (bool, error) {
	record.SchemaVersion = schemaVersion
	record.AttemptedAt = record.AttemptedAt.UTC()
	if record.CardID == "" || record.PINRevision == "" || record.AttemptedAt.IsZero() {
		return false, errors.New("invalid SIM PIN attempt record")
	}
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketAttempts)
		if wire := bucket.Get([]byte(record.CardID)); wire != nil {
			var prior Record
			if json.Unmarshal(wire, &prior) != nil || prior.SchemaVersion != schemaVersion {
				return errors.New("invalid persisted SIM PIN attempt")
			}
			if prior.PINRevision == record.PINRevision {
				return nil
			}
		}
		wire, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(record.CardID), wire); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

func (store *Store) Clear(cardID, pinRevision string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketAttempts)
		wire := bucket.Get([]byte(cardID))
		if wire == nil {
			return nil
		}
		var prior Record
		if json.Unmarshal(wire, &prior) != nil || prior.SchemaVersion != schemaVersion {
			return errors.New("invalid persisted SIM PIN attempt")
		}
		if prior.PINRevision != pinRevision {
			return errors.New("SIM PIN attempt identity changed")
		}
		return bucket.Delete([]byte(cardID))
	})
}

func (store *Store) Close() error { return store.db.Close() }
