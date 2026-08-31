// Package rawcapture owns local, durable whole-Modem mode and capture state.
// Core never writes this store: only the Agent's loopback control API can
// change a desired equipment+ICCID pair between adapted and raw.
package rawcapture

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
	bolt "go.etcd.io/bbolt"
)

const SchemaVersion uint64 = 1

type Stage string

const (
	StageCapturePending Stage = "capture_pending"
	StageReserved       Stage = "capture_reserved"
	StageReleasePending Stage = "release_pending_guarded"
)

type Pair struct {
	EquipmentID string `json:"equipment_id"`
	CardID      string `json:"iccid"`
}

type Proof struct {
	Pair
	AttachmentID      string `json:"attachment_id"`
	SessionGeneration string `json:"session_generation"`
	PhysicalID        string `json:"-"`
}

type Record struct {
	SchemaVersion     uint64        `json:"schema_version"`
	Pair              Pair          `json:"pair"`
	CaptureGeneration string        `json:"capture_generation"`
	AttachmentID      string        `json:"attachment_id"`
	SessionGeneration string        `json:"session_generation"`
	PhysicalID        string        `json:"physical_id,omitempty"`
	Stage             Stage         `json:"stage"`
	Device            rawusb.Device `json:"device"`
	UpdatedAt         time.Time     `json:"updated_at"`
}

type Snapshot struct {
	Desired  []Pair   `json:"desired"`
	Captures []Record `json:"captures"`
}

type Store struct{ db *bolt.DB }

var (
	metadataBucket = []byte("metadata")
	desiredBucket  = []byte("desired_raw_pairs")
	captureBucket  = []byte("captures")
	schemaKey      = []byte("schema")
	ErrModeChanged = errors.New("raw modem mode changed")
	ErrNotFound    = errors.New("raw modem capture not found")
)

func Open(path string, timeout time.Duration) (*Store, error) {
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid raw capture store configuration")
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
		metadata, err := tx.CreateBucketIfNotExists(metadataBucket)
		if err != nil {
			return err
		}
		for _, bucket := range [][]byte{desiredBucket, captureBucket} {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return err
			}
		}
		stored := metadata.Get(schemaKey)
		if stored == nil {
			var encoded [8]byte
			binary.BigEndian.PutUint64(encoded[:], SchemaVersion)
			return metadata.Put(schemaKey, encoded[:])
		}
		if len(stored) != 8 || binary.BigEndian.Uint64(stored) != SchemaVersion {
			return errors.New("unsupported raw capture store schema")
		}
		return nil
	})
}

func (store *Store) Snapshot() (Snapshot, error) {
	result := Snapshot{Desired: []Pair{}, Captures: []Record{}}
	err := store.db.View(func(tx *bolt.Tx) error {
		if err := tx.Bucket(desiredBucket).ForEach(func(_, payload []byte) error {
			var pair Pair
			if json.Unmarshal(payload, &pair) != nil || validatePair(pair) != nil {
				return errors.New("stored raw modem mode is invalid")
			}
			result.Desired = append(result.Desired, pair)
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket(captureBucket).ForEach(func(_, payload []byte) error {
			record, err := decodeRecord(payload)
			if err != nil {
				return err
			}
			result.Captures = append(result.Captures, record)
			return nil
		})
	})
	sort.Slice(result.Desired, func(i, j int) bool { return result.Desired[i].EquipmentID < result.Desired[j].EquipmentID })
	sort.Slice(result.Captures, func(i, j int) bool { return result.Captures[i].Pair.EquipmentID < result.Captures[j].Pair.EquipmentID })
	return result, err
}

// SetRaw is the only durable adapted -> raw intent mutation.
func (store *Store) SetRaw(pair Pair) error {
	if err := validatePair(pair); err != nil {
		return err
	}
	payload, _ := json.Marshal(pair)
	return store.db.Update(func(tx *bolt.Tx) error {
		desired := tx.Bucket(desiredBucket)
		if owner := desired.Get([]byte(pair.EquipmentID)); owner != nil {
			var current Pair
			if json.Unmarshal(owner, &current) != nil || current != pair {
				return ErrModeChanged
			}
			return nil
		}
		cursor := desired.Cursor()
		for _, value := cursor.First(); value != nil; _, value = cursor.Next() {
			var current Pair
			if json.Unmarshal(value, &current) != nil {
				return errors.New("stored raw modem mode is invalid")
			}
			if current.CardID == pair.CardID {
				return ErrModeChanged
			}
		}
		return desired.Put([]byte(pair.EquipmentID), payload)
	})
}

// SetAdapted atomically removes local raw intent and marks any exact capture
// for guarded release before the platform is touched.
func (store *Store) SetAdapted(pair Pair, now time.Time) error {
	if err := validatePair(pair); err != nil {
		return err
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		desired := tx.Bucket(desiredBucket)
		if payload := desired.Get([]byte(pair.EquipmentID)); payload != nil {
			var current Pair
			if json.Unmarshal(payload, &current) != nil || current != pair {
				return ErrModeChanged
			}
			if err := desired.Delete([]byte(pair.EquipmentID)); err != nil {
				return err
			}
		}
		captures := tx.Bucket(captureBucket)
		if payload := captures.Get([]byte(pair.EquipmentID)); payload != nil {
			record, err := decodeRecord(payload)
			if err != nil || record.Pair != pair {
				return ErrModeChanged
			}
			record.Stage, record.UpdatedAt = StageReleasePending, now.UTC()
			encoded, _ := json.Marshal(record)
			return captures.Put([]byte(pair.EquipmentID), encoded)
		}
		return nil
	})
}

func (store *Store) ArmCapture(proof Proof, generation string, now time.Time) (Record, error) {
	if err := validateProof(proof); err != nil || !validIdentifier(generation) {
		return Record{}, errors.New("invalid raw capture proof")
	}
	record := Record{SchemaVersion: SchemaVersion, Pair: proof.Pair, CaptureGeneration: generation,
		AttachmentID: proof.AttachmentID, SessionGeneration: proof.SessionGeneration,
		PhysicalID: proof.PhysicalID, Stage: StageCapturePending, UpdatedAt: now.UTC()}
	err := store.db.Update(func(tx *bolt.Tx) error {
		desired := tx.Bucket(desiredBucket).Get([]byte(proof.EquipmentID))
		var current Pair
		if json.Unmarshal(desired, &current) != nil || current != proof.Pair {
			return ErrModeChanged
		}
		bucket := tx.Bucket(captureBucket)
		if payload := bucket.Get([]byte(proof.EquipmentID)); payload != nil {
			stored, err := decodeRecord(payload)
			if err != nil || stored.Pair != proof.Pair {
				return ErrModeChanged
			}
			if stored.Stage == StageCapturePending {
				record.CaptureGeneration = stored.CaptureGeneration
				encoded, _ := json.Marshal(record)
				return bucket.Put([]byte(proof.EquipmentID), encoded)
			}
			record = stored
			return nil
		}
		encoded, _ := json.Marshal(record)
		return bucket.Put([]byte(proof.EquipmentID), encoded)
	})
	return record, err
}

func (store *Store) CompleteCapture(pair Pair, generation string, device rawusb.Device, now time.Time) (Record, error) {
	var result Record
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(captureBucket)
		record, err := decodeRecord(bucket.Get([]byte(pair.EquipmentID)))
		if err != nil || record.Pair != pair || record.CaptureGeneration != generation || record.Stage != StageCapturePending {
			return ErrModeChanged
		}
		if err := validateDevice(device); err != nil {
			return err
		}
		record.Device, record.Stage, record.UpdatedAt = device, StageReserved, now.UTC()
		encoded, _ := json.Marshal(record)
		if err := bucket.Put([]byte(pair.EquipmentID), encoded); err != nil {
			return err
		}
		result = record
		return nil
	})
	return result, err
}

func (store *Store) RefreshCaptureDevice(pair Pair, generation string, device rawusb.Device, now time.Time) (Record, error) {
	var result Record
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(captureBucket)
		record, err := decodeRecord(bucket.Get([]byte(pair.EquipmentID)))
		if err != nil || record.Pair != pair || record.CaptureGeneration != generation || record.Stage != StageReserved {
			return ErrModeChanged
		}
		if err := validateDevice(device); err != nil {
			return err
		}
		record.Device, record.UpdatedAt = device, now.UTC()
		encoded, _ := json.Marshal(record)
		if err := bucket.Put([]byte(pair.EquipmentID), encoded); err != nil {
			return err
		}
		result = record
		return nil
	})
	return result, err
}

func (store *Store) RearmCapture(proof Proof, generation string, now time.Time) (Record, error) {
	if err := validateProof(proof); err != nil || !validIdentifier(generation) {
		return Record{}, errors.New("invalid raw recapture proof")
	}
	record := Record{SchemaVersion: SchemaVersion, Pair: proof.Pair, CaptureGeneration: generation,
		AttachmentID: proof.AttachmentID, SessionGeneration: proof.SessionGeneration,
		PhysicalID: proof.PhysicalID, Stage: StageCapturePending, UpdatedAt: now.UTC()}
	err := store.db.Update(func(tx *bolt.Tx) error {
		var desired Pair
		if json.Unmarshal(tx.Bucket(desiredBucket).Get([]byte(proof.EquipmentID)), &desired) != nil || desired != proof.Pair {
			return ErrModeChanged
		}
		bucket := tx.Bucket(captureBucket)
		stored, err := decodeRecord(bucket.Get([]byte(proof.EquipmentID)))
		if err != nil || stored.Pair != proof.Pair || stored.Stage != StageReserved {
			return ErrModeChanged
		}
		encoded, _ := json.Marshal(record)
		return bucket.Put([]byte(proof.EquipmentID), encoded)
	})
	return record, err
}

func (store *Store) ClearReleased(pair Pair, generation string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(captureBucket)
		record, err := decodeRecord(bucket.Get([]byte(pair.EquipmentID)))
		if err != nil {
			return err
		}
		if record.Pair != pair || record.CaptureGeneration != generation || record.Stage != StageReleasePending {
			return ErrModeChanged
		}
		return bucket.Delete([]byte(pair.EquipmentID))
	})
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func decodeRecord(payload []byte) (Record, error) {
	if payload == nil {
		return Record{}, ErrNotFound
	}
	var record Record
	if json.Unmarshal(payload, &record) != nil || validateRecord(record) != nil {
		return Record{}, errors.New("stored raw capture is invalid")
	}
	return record, nil
}

func validateRecord(record Record) error {
	if record.SchemaVersion != SchemaVersion || validatePair(record.Pair) != nil || !validIdentifier(record.CaptureGeneration) ||
		!validIdentifier(record.AttachmentID) || !validIdentifier(record.SessionGeneration) || record.UpdatedAt.IsZero() {
		return errors.New("invalid raw capture record")
	}
	switch record.Stage {
	case StageCapturePending:
		if record.Device.BusID != "" {
			return errors.New("pending capture contains a USB device")
		}
	case StageReserved, StageReleasePending:
		return validateDevice(record.Device)
	default:
		return errors.New("invalid raw capture stage")
	}
	return nil
}

func validateProof(proof Proof) error {
	if validatePair(proof.Pair) != nil || !validIdentifier(proof.AttachmentID) ||
		!validIdentifier(proof.SessionGeneration) || strings.TrimSpace(proof.PhysicalID) == "" {
		return errors.New("invalid raw capture proof")
	}
	return nil
}

func validatePair(pair Pair) error {
	if !digits(pair.EquipmentID, 14, 16) || !digits(pair.CardID, 4, 32) {
		return errors.New("raw modem mode requires an exact equipment ID and ICCID")
	}
	return nil
}

func validateDevice(device rawusb.Device) error {
	if strings.TrimSpace(device.BusID) == "" || len(device.BusID) > 128 || device.VendorID == 0 || device.ProductID == 0 ||
		len(device.StableID) > 512 || len(device.Serial) > 512 {
		return errors.New("invalid captured USB identity")
	}
	switch device.Backend {
	case "":
		if device.InstanceID != "" || device.PersistentID != "" {
			return errors.New("platform capture identity has no backend")
		}
	case "windows-usbipd-v1":
		if strings.TrimSpace(device.InstanceID) == "" || len(device.InstanceID) > 512 ||
			strings.TrimSpace(device.PersistentID) == "" || len(device.PersistentID) > 128 {
			return errors.New("Windows persistent capture identity is incomplete")
		}
	default:
		return errors.New("unknown captured USB backend")
	}
	return nil
}

func digits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			continue
		}
		return false
	}
	return true
}

func (pair Pair) key() string { return fmt.Sprintf("%s/%s", pair.EquipmentID, pair.CardID) }
