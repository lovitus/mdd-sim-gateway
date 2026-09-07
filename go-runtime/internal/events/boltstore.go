package events

import (
	"bytes"
	"crypto/rand"
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

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	bolt "go.etcd.io/bbolt"
)

const BoltSchemaVersion uint64 = 1

var (
	ErrEventIDConflict = errors.New("event ID already belongs to a different event")
	ErrOlderCheckpoint = errors.New("producer checkpoint sequence is older than current")
	ErrStoreSchema     = errors.New("unsupported event store schema")
)

var (
	bucketMetadata    = []byte("metadata")
	bucketBindings    = []byte("producer_bindings")
	bucketStreams     = []byte("generation_streams")
	bucketRecords     = []byte("records")
	bucketEventIDs    = []byte("event_ids")
	bucketCheckpoints = []byte("producer_checkpoints")
	bucketPurgedLines = []byte("purged_lines_v1")
	keySchema         = []byte("schema_version")
)

// BoltStore durably binds producer generations and appends their accepted
// records. Activate is the trusted Core operation for replacing a binding and
// accepting its first event in one transaction. Producers use Accept after
// activation and cannot authorize themselves.
type BoltStore struct {
	availabilitySession string
	db                  *bolt.DB
	maxRecordBytes      int
	closeOnce           sync.Once
	closeErr            error
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
	store := &BoltStore{db: db, maxRecordBytes: DefaultMaxRecordBytes, availabilitySession: rand.Text()}
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
		for _, name := range [][]byte{bucketMetadata, bucketBindings, bucketStreams, bucketRecords, bucketEventIDs, bucketCheckpoints, bucketPurgedLines, bucketAvailability} {
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
		var err error
		accepted, err = store.appendEventTx(tx, event, receivedAt, activate)
		return err
	})
	if err != nil {
		return Record{}, err
	}
	return accepted, nil
}

// AcceptSnapshot atomically persists changed layer events and overwrites one
// complete-snapshot checkpoint. The first snapshot of a generation activates
// its durable producer binding; later snapshots cannot revive an old one.
func (store *BoltStore) AcceptSnapshot(events []Event, checkpoint ProducerCheckpoint) ([]Record, ProducerCheckpoint, error) {
	if err := checkpoint.Validate(); err != nil {
		return nil, ProducerCheckpoint{}, err
	}
	checkpoint.LineID = strings.TrimSpace(checkpoint.LineID)
	checkpoint.ProducerID = strings.TrimSpace(checkpoint.ProducerID)
	checkpoint.Generation = strings.TrimSpace(checkpoint.Generation)
	checkpoint.ObservedAt = checkpoint.ObservedAt.UTC()
	checkpoint.ReceivedAt = checkpoint.ReceivedAt.UTC()
	seenLayers := make(map[string]struct{}, len(events))
	checkpointLayers := make(map[state.Layer]struct{}, len(checkpoint.Layers))
	for _, layer := range checkpoint.Layers {
		checkpointLayers[layer] = struct{}{}
	}
	for _, event := range events {
		if err := validateEvent(event); err != nil || event.LineID != checkpoint.LineID ||
			event.ProducerRole != checkpoint.ProducerRole || event.ProducerID != checkpoint.ProducerID ||
			event.Generation != checkpoint.Generation || event.Sequence != checkpoint.Sequence {
			return nil, ProducerCheckpoint{}, ErrInvalidEvent
		}
		if _, covered := checkpointLayers[event.Layer]; !covered {
			return nil, ProducerCheckpoint{}, ErrInvalidEvent
		}
		if _, duplicate := seenLayers[string(event.Layer)]; duplicate {
			return nil, ProducerCheckpoint{}, ErrInvalidEvent
		}
		seenLayers[string(event.Layer)] = struct{}{}
	}

	accepted := make([]Record, 0, len(events))
	storedCheckpoint := checkpoint
	err := store.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketPurgedLines).Get([]byte(checkpoint.LineID)) != nil {
			return errors.New("event line was permanently deleted")
		}
		binding := []byte(bindingKey(checkpoint.LineID, checkpoint.ProducerRole))
		fingerprint := checkpointFingerprint(checkpoint)
		expected := tx.Bucket(bucketBindings).Get(binding)
		activate := !bytes.Equal(expected, []byte(fingerprint))
		if activate && len(events) != len(checkpoint.Layers) {
			return ErrUnauthorizedProducer
		}
		for index, event := range events {
			record, err := store.appendEventTx(tx, event, checkpoint.ReceivedAt, activate && index == 0)
			if err != nil {
				return err
			}
			accepted = append(accepted, record)
		}
		if !activate && !bytes.Equal(tx.Bucket(bucketBindings).Get(binding), []byte(fingerprint)) {
			return ErrUnauthorizedProducer
		}
		checkpoints := tx.Bucket(bucketCheckpoints)
		key := binding
		if encoded := checkpoints.Get(key); encoded != nil {
			current, err := decodeCheckpoint(encoded, store.maxRecordBytes)
			if err != nil {
				return err
			}
			if checkpointFingerprint(current) == fingerprint {
				if checkpoint.Sequence < current.Sequence {
					return ErrOlderCheckpoint
				}
				if checkpoint.Sequence == current.Sequence {
					if checkpoint.ObservedAt != current.ObservedAt || !reflect.DeepEqual(checkpoint.Layers, current.Layers) {
						return ErrEventIDConflict
					}
					storedCheckpoint = current
					return nil
				}
			}
		}
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			return err
		}
		if len(encoded) > store.maxRecordBytes {
			return ErrInvalidEvent
		}
		if err := store.recordAvailabilityTx(tx, events, checkpoint); err != nil {
			return err
		}
		return checkpoints.Put(key, encoded)
	})
	if err != nil {
		return nil, ProducerCheckpoint{}, err
	}
	return accepted, storedCheckpoint, nil
}

func (store *BoltStore) appendEventTx(tx *bolt.Tx, event Event, receivedAt time.Time, activate bool) (Record, error) {
	if tx.Bucket(bucketPurgedLines).Get([]byte(strings.TrimSpace(event.LineID))) != nil {
		return Record{}, errors.New("event line was permanently deleted")
	}
	bindings := tx.Bucket(bucketBindings)
	records := tx.Bucket(bucketRecords)
	eventIDs := tx.Bucket(bucketEventIDs)
	streams := tx.Bucket(bucketStreams)
	binding := []byte(bindingKey(strings.TrimSpace(event.LineID), event.ProducerRole))
	fingerprint := producerFingerprint(event)
	expected := bindings.Get(binding)

	if recordKey := eventIDs.Get([]byte(strings.TrimSpace(event.EventID))); recordKey != nil {
		if !bytes.Equal(expected, []byte(fingerprint)) {
			return Record{}, ErrUnauthorizedProducer
		}
		stored, err := decodeRecord(records.Get(recordKey), store.maxRecordBytes)
		if err != nil {
			return Record{}, err
		}
		if !reflect.DeepEqual(stored.Event, event) {
			return Record{}, ErrEventIDConflict
		}
		return stored, nil
	}

	if activate {
		if err := bindings.Put(binding, []byte(fingerprint)); err != nil {
			return Record{}, fmt.Errorf("replace producer binding: %w", err)
		}
	} else if !bytes.Equal(expected, []byte(fingerprint)) {
		return Record{}, ErrUnauthorizedProducer
	}

	streamKey := []byte(strings.TrimSpace(event.LineID) + "\x00" + string(event.Layer))
	clock, err := decodeClock(streams.Get(streamKey))
	if err != nil {
		return Record{}, err
	}
	epoch, err := assignEpoch(&clock, fingerprint)
	if err != nil {
		return Record{}, err
	}
	clockBytes, err := json.Marshal(clock)
	if err != nil {
		return Record{}, fmt.Errorf("encode generation clock: %w", err)
	}
	if err := streams.Put(streamKey, clockBytes); err != nil {
		return Record{}, fmt.Errorf("persist generation clock: %w", err)
	}

	accepted := Record{ReceivedAt: receivedAt, Epoch: epoch, Event: event}
	encoded, err := json.Marshal(accepted)
	if err != nil {
		return Record{}, fmt.Errorf("encode event record: %w", err)
	}
	if len(encoded) > store.maxRecordBytes {
		return Record{}, fmt.Errorf("%w: record exceeds %d bytes", ErrInvalidEvent, store.maxRecordBytes)
	}
	sequence, err := records.NextSequence()
	if err != nil {
		return Record{}, fmt.Errorf("allocate event record sequence: %w", err)
	}
	recordKey := encodeUint64(sequence)
	if err := records.Put(recordKey, encoded); err != nil {
		return Record{}, fmt.Errorf("persist event record: %w", err)
	}
	if err := eventIDs.Put([]byte(strings.TrimSpace(event.EventID)), recordKey); err != nil {
		return Record{}, fmt.Errorf("persist event ID: %w", err)
	}
	return accepted, nil
}

type LinePurger struct {
	store  *BoltStore
	replay *Replay
}

func NewLinePurger(store *BoltStore, replay *Replay) (*LinePurger, error) {
	if store == nil || replay == nil {
		return nil, errors.New("invalid event line purger")
	}
	return &LinePurger{store: store, replay: replay}, nil
}

func (purger *LinePurger) PurgeLine(lineID string) error {
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return errors.New("invalid event purge line identity")
	}
	err := purger.store.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketPurgedLines).Put([]byte(lineID), []byte{1}); err != nil {
			return err
		}
		if err := tx.Bucket(bucketAvailability).Delete([]byte(lineID)); err != nil {
			return err
		}
		records, ids := tx.Bucket(bucketRecords), tx.Bucket(bucketEventIDs)
		var recordKeys, eventIDs [][]byte
		if err := records.ForEach(func(key, value []byte) error {
			record, err := decodeRecord(value, purger.store.maxRecordBytes)
			if err != nil {
				return err
			}
			if record.Event.LineID == lineID {
				recordKeys = append(recordKeys, append([]byte(nil), key...))
				eventIDs = append(eventIDs, []byte(record.Event.EventID))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range recordKeys {
			if err := records.Delete(key); err != nil {
				return err
			}
		}
		for _, key := range eventIDs {
			if err := ids.Delete(key); err != nil {
				return err
			}
		}
		prefix := []byte(lineID + "\x00")
		for _, name := range [][]byte{bucketBindings, bucketStreams, bucketCheckpoints} {
			bucket := tx.Bucket(name)
			cursor := bucket.Cursor()
			var keys [][]byte
			for key, _ := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, _ = cursor.Next() {
				keys = append(keys, append([]byte(nil), key...))
			}
			for _, key := range keys {
				if err := bucket.Delete(key); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	purger.replay.RemoveLine(lineID)
	return nil
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
		checkpoints := tx.Bucket(bucketCheckpoints).Cursor()
		index = 0
		for _, value := checkpoints.First(); value != nil; _, value = checkpoints.Next() {
			index++
			checkpoint, err := decodeCheckpoint(value, store.maxRecordBytes)
			if err != nil {
				return fmt.Errorf("stored checkpoint %d: %w", index, err)
			}
			if err := replay.Confirm(checkpoint); err != nil {
				return fmt.Errorf("apply stored checkpoint %d: %w", index, err)
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

func checkpointFingerprint(checkpoint ProducerCheckpoint) string {
	return string(checkpoint.ProducerRole) + "\x00" + strings.TrimSpace(checkpoint.ProducerID) + "\x00" + strings.TrimSpace(checkpoint.Generation)
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

func decodeCheckpoint(value []byte, maximum int) (ProducerCheckpoint, error) {
	if len(value) == 0 || len(value) > maximum {
		return ProducerCheckpoint{}, ErrInvalidEvent
	}
	var checkpoint ProducerCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return ProducerCheckpoint{}, err
	}
	if err := requireJSONEnd(decoder); err != nil {
		return ProducerCheckpoint{}, err
	}
	if err := checkpoint.Validate(); err != nil {
		return ProducerCheckpoint{}, err
	}
	return checkpoint, nil
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
