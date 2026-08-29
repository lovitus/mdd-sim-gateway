package egressconfig

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	metadataBucket = []byte("metadata")
	configBucket   = []byte("config")
	schemaKey      = []byte("schema")
	revisionKey    = []byte("revision")
	configKey      = []byte("desired")
	importKey      = []byte("legacy_import")
	ErrNotImported = errors.New("country exit configuration has not been imported")
	ErrNotEmpty    = errors.New("country exit configuration is not empty")
	ErrRevision    = errors.New("country exit configuration revision does not match")
)

type ImportReceipt struct {
	SourceSHA256 string    `json:"source_sha256"`
	ImportedAt   time.Time `json:"imported_at"`
}

type Store struct{ db *bolt.DB }

func Open(path string, timeout time.Duration) (*Store, error) {
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid country exit store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	created := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		created = true
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("open country exit store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	store := &Store{db: db}
	if err := store.initialize(); err != nil {
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
	return store.db.Update(func(transaction *bolt.Tx) error {
		metadata, err := transaction.CreateBucketIfNotExists(metadataBucket)
		if err != nil {
			return err
		}
		if schema := metadata.Get(schemaKey); schema == nil {
			if err := metadata.Put(schemaKey, uint64Bytes(SchemaVersion)); err != nil {
				return err
			}
		} else if bytesUint64(schema) != SchemaVersion {
			return fmt.Errorf("unsupported country exit store schema %d", bytesUint64(schema))
		}
		if _, err := transaction.CreateBucketIfNotExists(configBucket); err != nil {
			return err
		}
		if metadata.Get(revisionKey) == nil {
			return metadata.Put(revisionKey, uint64Bytes(1))
		}
		return nil
	})
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *Store) Snapshot() (Snapshot, error) {
	var result Snapshot
	err := store.db.View(func(transaction *bolt.Tx) error {
		payload := transaction.Bucket(configBucket).Get(configKey)
		if payload == nil {
			return ErrNotImported
		}
		if err := json.Unmarshal(payload, &result.Config); err != nil {
			return errors.New("stored country exit configuration is corrupt")
		}
		if err := result.Config.normalizeAndValidate(); err != nil {
			return errors.New("stored country exit configuration is invalid")
		}
		result.SchemaVersion = SchemaVersion
		result.Revision = bytesUint64(transaction.Bucket(metadataBucket).Get(revisionKey))
		return nil
	})
	result.Config = cloneConfig(result.Config)
	return result, err
}

func (store *Store) PutExpected(input Config, expectedRevision uint64) (Snapshot, error) {
	config := cloneConfig(input)
	if err := config.normalizeAndValidate(); err != nil {
		return Snapshot{}, err
	}
	payload, err := json.Marshal(config)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{SchemaVersion: SchemaVersion, Config: config}
	err = store.db.Update(func(transaction *bolt.Tx) error {
		metadata := transaction.Bucket(metadataBucket)
		result.Revision = bytesUint64(metadata.Get(revisionKey))
		if result.Revision != expectedRevision {
			return ErrRevision
		}
		current := transaction.Bucket(configBucket).Get(configKey)
		if current == nil {
			return ErrNotImported
		}
		if bytes.Equal(current, payload) {
			return nil
		}
		if err := transaction.Bucket(configBucket).Put(configKey, payload); err != nil {
			return err
		}
		result.Revision++
		return metadata.Put(revisionKey, uint64Bytes(result.Revision))
	})
	return result, err
}

func (store *Store) ImportEmpty(config Config, receipt ImportReceipt) error {
	config = cloneConfig(config)
	if err := config.normalizeAndValidate(); err != nil {
		return err
	}
	if len(receipt.SourceSHA256) != 64 || receipt.ImportedAt.IsZero() {
		return errors.New("invalid country exit legacy import receipt")
	}
	payload, err := json.Marshal(config)
	if err != nil {
		return err
	}
	receiptPayload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return store.db.Update(func(transaction *bolt.Tx) error {
		if transaction.Bucket(configBucket).Get(configKey) != nil || transaction.Bucket(metadataBucket).Get(importKey) != nil {
			return ErrNotEmpty
		}
		if err := transaction.Bucket(configBucket).Put(configKey, payload); err != nil {
			return err
		}
		metadata := transaction.Bucket(metadataBucket)
		if err := metadata.Put(importKey, receiptPayload); err != nil {
			return err
		}
		revision := bytesUint64(metadata.Get(revisionKey)) + 1
		return metadata.Put(revisionKey, uint64Bytes(revision))
	})
}

func uint64Bytes(value uint64) []byte {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, value)
	return payload
}

func bytesUint64(payload []byte) uint64 {
	if len(payload) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(payload)
}

func syncParentDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
