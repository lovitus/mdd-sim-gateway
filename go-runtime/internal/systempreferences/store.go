// Package systempreferences owns small, durable product-wide user settings.
// Per-line desired state, hardware policy and runtime health do not belong here.
package systempreferences

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

const (
	SchemaVersion            = 1
	DefaultCallAudioBufferMS = 500
	MinCallAudioBufferMS     = 100
	MaxCallAudioBufferMS     = 2000
)

var (
	metadataBucket = []byte("metadata")
	valueBucket    = []byte("preferences")
	schemaKey      = []byte("schema")
	revisionKey    = []byte("revision")
	valueKey       = []byte("current")
	ErrRevision    = errors.New("system preference revision does not match")
)

type Preferences struct {
	CallAudioBufferMS int `json:"call_audio_buffer_ms"`
}

type Snapshot struct {
	SchemaVersion int         `json:"schema_version"`
	Revision      uint64      `json:"revision"`
	Preferences   Preferences `json:"preferences"`
}

type Store struct{ db *bolt.DB }

func Open(path string, timeout time.Duration) (*Store, error) {
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid system preference store configuration")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, err
		}
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("open system preference store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	store := &Store{db: db}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

func (store *Store) initialize() error {
	defaults, _ := json.Marshal(Preferences{CallAudioBufferMS: DefaultCallAudioBufferMS})
	return store.db.Update(func(tx *bolt.Tx) error {
		metadata, err := tx.CreateBucketIfNotExists(metadataBucket)
		if err != nil {
			return err
		}
		if schema := metadata.Get(schemaKey); schema == nil {
			if err := metadata.Put(schemaKey, uint64Bytes(SchemaVersion)); err != nil {
				return err
			}
		} else if bytesUint64(schema) != SchemaVersion {
			return fmt.Errorf("unsupported system preference schema %d", bytesUint64(schema))
		}
		values, err := tx.CreateBucketIfNotExists(valueBucket)
		if err != nil {
			return err
		}
		if metadata.Get(revisionKey) == nil {
			if err := metadata.Put(revisionKey, uint64Bytes(1)); err != nil {
				return err
			}
		}
		if values.Get(valueKey) == nil {
			return values.Put(valueKey, defaults)
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
	result := Snapshot{SchemaVersion: SchemaVersion}
	err := store.db.View(func(tx *bolt.Tx) error {
		if err := json.Unmarshal(tx.Bucket(valueBucket).Get(valueKey), &result.Preferences); err != nil ||
			validate(result.Preferences) != nil {
			return errors.New("stored system preferences are invalid")
		}
		result.Revision = bytesUint64(tx.Bucket(metadataBucket).Get(revisionKey))
		return nil
	})
	return result, err
}

func (store *Store) PutExpected(input Preferences, expected uint64) (Snapshot, error) {
	if err := validate(input); err != nil {
		return Snapshot{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{SchemaVersion: SchemaVersion, Preferences: input}
	err = store.db.Update(func(tx *bolt.Tx) error {
		metadata := tx.Bucket(metadataBucket)
		result.Revision = bytesUint64(metadata.Get(revisionKey))
		if result.Revision != expected {
			return ErrRevision
		}
		current := tx.Bucket(valueBucket).Get(valueKey)
		if bytes.Equal(current, payload) {
			return nil
		}
		if err := tx.Bucket(valueBucket).Put(valueKey, payload); err != nil {
			return err
		}
		result.Revision++
		return metadata.Put(revisionKey, uint64Bytes(result.Revision))
	})
	return result, err
}

func validate(value Preferences) error {
	if value.CallAudioBufferMS < MinCallAudioBufferMS || value.CallAudioBufferMS > MaxCallAudioBufferMS {
		return errors.New("call audio buffer must be between 100 and 2000 milliseconds")
	}
	return nil
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
