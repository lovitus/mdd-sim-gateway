package events

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const BoltSchemaVersion uint64 = 1

var (
	ErrEventIDConflict = errors.New("event ID already belongs to a different event")
	ErrStoreSchema     = errors.New("unsupported event store schema")
)

var (
	bucketMetadata = []byte("metadata")
	bucketBindings = []byte("producer_bindings")
	bucketStreams  = []byte("generation_streams")
	bucketRecords  = []byte("records")
	bucketEventIDs = []byte("event_ids")
	keySchema      = []byte("schema_version")
)

// BoltStore durably binds producer generations and appends their accepted
// records. Activate is the trusted Core operation for replacing a binding and
// accepting its first event in one transaction. Producers use Accept after
// activation and cannot authorize themselves.
type BoltStore struct {
	db             *bolt.DB
	maxRecordBytes int
	closeOnce      sync.Once
	closeErr       error
}

func OpenBoltStore(path string, lockTimeout time.Duration) (*BoltStore, error) {
	path = strings.TrimSpace(path)
	if path == "" || lockTimeout <= 0 {
		return nil, errors.New("invalid bbolt store configuration")
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, fmt.Errorf("stat bbolt store: %w", statErr)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: lockTimeout})
	if err != nil {
		return nil, fmt.Errorf("open bbolt store: %w", err)
	}
	store := &BoltStore{db: db, maxRecordBytes: DefaultMaxRecordBytes}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("protect bbolt store: %w", err), db.Close())
	}
	if created {
		if err := syncParentDirectory(filepath.Dir(path)); err != nil {
			return nil, errors.Join(fmt.Errorf("sync bbolt parent directory: %w", err), db.Close())
		}
	}
	return store, nil
}

func (store *BoltStore) initialize() error {
	return store.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMetadata, bucketBindings, bucketStreams, bucketRecords, bucketEventIDs} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create bucket %q: %w", name, err)
			}
		}
		metadata := tx.Bucket(bucketMetadata)
		stored := metadata.Get(keySchema)
		if stored == nil {
			return metadata.Put(keySchema, encodeUint64(BoltSchemaVersion))
		}
		if len(stored) != 8 || binary.BigEndian.Uint64(stored) != BoltSchemaVersion {
			return fmt.Errorf("%w: found %x, support %d", ErrStoreSchema, stored, BoltSchemaVersion)
		}
		return nil
	})
}

func (store *BoltStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.closeOnce.Do(func() { store.closeErr = store.db.Close() })
	return store.closeErr
}

// Activate atomically replaces the role binding and persists its first event.
// Only the trusted Core coordinator may expose this operation.
func (store *BoltStore) Activate(event Event, receivedAt time.Time) (Record, error) {
	return store.append(event, receivedAt, true)
}

// Accept persists an event only when its exact producer and generation match
// the current durable binding.
func (store *BoltStore) Accept(event Event, receivedAt time.Time) (Record, error) {
	return store.append(event, receivedAt, false)
}

func (store *BoltStore) append(event Event, receivedAt time.Time, activate bool) (Record, error) {
	if err := validateEvent(event); err != nil || receivedAt.IsZero() {
		if err != nil {
			return Record{}, err
		}
		return Record{}, ErrInvalidEvent
	}
	receivedAt = receivedAt.UTC()
	var accepted Record
	err := store.db.Update(func(tx *bolt.Tx) error {
		bindings := tx.Bucket(bucketBindings)
		records := tx.Bucket(bucketRecords)
		eventIDs := tx.Bucket(bucketEventIDs)
		streams := tx.Bucket(bucketStreams)
		binding := []byte(bindingKey(strings.TrimSpace(event.LineID), event.ProducerRole))
		fingerprint := producerFingerprint(event)
		expected := bindings.Get(binding)

		if recordKey := eventIDs.Get([]byte(strings.TrimSpace(event.EventID))); recordKey != nil {
			if !bytes.Equal(expected, []byte(fingerprint)) {
				return ErrUnauthorizedProducer
			}
			stored, err := decodeRecord(records.Get(recordKey), store.maxRecordBytes)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(stored.Event, event) {
				return ErrEventIDConflict
			}
			accepted = stored
			return nil
		}

		if activate {
			if err := bindings.Put(binding, []byte(fingerprint)); err != nil {
				return fmt.Errorf("replace producer binding: %w", err)
			}
		} else if !bytes.Equal(expected, []byte(fingerprint)) {
			return ErrUnauthorizedProducer
		}

		streamKey := []byte(strings.TrimSpace(event.LineID) + "\x00" + string(event.Layer))
		clock, err := decodeClock(streams.Get(streamKey))
		if err != nil {
			return err
		}
		epoch, err := assignEpoch(&clock, fingerprint)
		if err != nil {
			return err
		}
		clockBytes, err := json.Marshal(clock)
		if err != nil {
			return fmt.Errorf("encode generation clock: %w", err)
		}
		if err := streams.Put(streamKey, clockBytes); err != nil {
			return fmt.Errorf("persist generation clock: %w", err)
		}

		accepted = Record{ReceivedAt: receivedAt, Epoch: epoch, Event: event}
		encoded, err := json.Marshal(accepted)
		if err != nil {
			return fmt.Errorf("encode event record: %w", err)
		}
		if len(encoded) > store.maxRecordBytes {
			return fmt.Errorf("%w: record exceeds %d bytes", ErrInvalidEvent, store.maxRecordBytes)
		}
		sequence, err := records.NextSequence()
		if err != nil {
			return fmt.Errorf("allocate event record sequence: %w", err)
		}
		recordKey := encodeUint64(sequence)
		if err := records.Put(recordKey, encoded); err != nil {
			return fmt.Errorf("persist event record: %w", err)
		}
		if err := eventIDs.Put([]byte(strings.TrimSpace(event.EventID)), recordKey); err != nil {
			return fmt.Errorf("persist event ID: %w", err)
		}
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return accepted, nil
}

func (store *BoltStore) ReplayInto(replay *Replay) error {
	if replay == nil {
		return ErrInvalidEvent
	}
	return store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketRecords).Cursor()
		index := 0
		for _, value := cursor.First(); value != nil; _, value = cursor.Next() {
			index++
			record, err := decodeRecord(value, store.maxRecordBytes)
			if err != nil {
				return fmt.Errorf("stored record %d: %w", index, err)
			}
			if _, err := replay.Apply(record); err != nil {
				return fmt.Errorf("apply stored record %d: %w", index, err)
			}
		}
		return nil
	})
}

func (store *BoltStore) ExportJSONLines(writer io.Writer) error {
	if writer == nil {
		return errors.New("event export writer is nil")
	}
	return store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketRecords).Cursor()
		for _, value := cursor.First(); value != nil; _, value = cursor.Next() {
			if len(value) > store.maxRecordBytes {
				return fmt.Errorf("stored record exceeds %d bytes", store.maxRecordBytes)
			}
			line := append(append([]byte(nil), value...), '\n')
			written, err := writer.Write(line)
			if err != nil {
				return fmt.Errorf("export event record: %w", err)
			}
			if written != len(line) {
				return fmt.Errorf("export event record: %w", io.ErrShortWrite)
			}
		}
		return nil
	})
}

func (store *BoltStore) Count() (uint64, error) {
	var count uint64
	err := store.db.View(func(tx *bolt.Tx) error {
		count = uint64(tx.Bucket(bucketRecords).Stats().KeyN)
		return nil
	})
	return count, err
}

func producerFingerprint(event Event) string {
	return string(event.ProducerRole) + "\x00" + strings.TrimSpace(event.ProducerID) + "\x00" + strings.TrimSpace(event.Generation)
}

func decodeClock(value []byte) (generationClock, error) {
	if value == nil {
		return generationClock{}, nil
	}
	var clock generationClock
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&clock); err != nil {
		return generationClock{}, fmt.Errorf("decode generation clock: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return generationClock{}, fmt.Errorf("decode generation clock: %w", err)
	}
	return clock, nil
}

func decodeRecord(value []byte, maximum int) (Record, error) {
	if len(value) == 0 || len(value) > maximum {
		return Record{}, fmt.Errorf("%w: invalid stored record size", ErrInvalidEvent)
	}
	var record Record
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("decode stored record: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Record{}, fmt.Errorf("decode stored record: %w", err)
	}
	if _, _, err := record.Observation(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func encodeUint64(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}

func requireJSONEnd(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
