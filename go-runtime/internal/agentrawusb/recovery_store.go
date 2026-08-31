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
	SchemaVersion           uint64               `json:"schema_version"`
	SourceAgentID           string               `json:"source_agent_id"`
	SourceProcessGeneration string               `json:"source_process_generation"`
	AttachmentID            string               `json:"attachment_id"`
	SessionGeneration       string               `json:"session_generation"`
	EquipmentID             string               `json:"equipment_id"`
	CardID                  string               `json:"card_id"`
	USBSessionID            string               `json:"usb_session_id"`
	USB                     *RecoveryUSBIdentity `json:"usb,omitempty"`
	ReleaseRequested        bool                 `json:"release_requested,omitempty"`
	ArmedAt                 time.Time            `json:"armed_at"`
}

type RecoveryUSBIdentity struct {
	StableID  string `json:"stable_id,omitempty"`
	BusID     string `json:"bus_id"`
	VendorID  uint16 `json:"vendor_id"`
	ProductID uint16 `json:"product_id"`
	Serial    string `json:"serial,omitempty"`
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

func (store *RecoveryStore) RecoveryForTarget(target SourceTarget) (RecoveryRecord, error) {
	var result RecoveryRecord
	err := store.db.View(func(tx *bolt.Tx) error {
		payload := tx.Bucket(recoveryRecordsBucket).Get([]byte(strings.TrimSpace(target.EquipmentID)))
		if payload == nil {
			return ErrRecoveryNotFound
		}
		current, err := decodeRecoveryRecord(payload)
		if err != nil {
			return err
		}
		if !recoveryRecordMatchesTarget(current, target) {
			return ErrRecoveryMismatch
		}
		result = current
		return nil
	})
	return result, err
}

// BindUSBIdentity durably records the exact USB identity only after the
// source owner has released AT and sing-usbip has opened that same physical
// parent. A crash after capture can therefore release the captured Windows
// device without guessing from VID/PID or a reader name.
func (store *RecoveryStore) BindUSBIdentity(target SourceTarget, identity RecoveryUSBIdentity) (RecoveryRecord, error) {
	identity.StableID = strings.TrimSpace(identity.StableID)
	identity.BusID = strings.TrimSpace(identity.BusID)
	identity.Serial = strings.TrimSpace(identity.Serial)
	if err := validateRecoveryUSBIdentity(identity); err != nil {
		return RecoveryRecord{}, err
	}
	var result RecoveryRecord
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recoveryRecordsBucket)
		payload := bucket.Get([]byte(strings.TrimSpace(target.EquipmentID)))
		if payload == nil {
			return ErrRecoveryNotFound
		}
		current, err := decodeRecoveryRecord(payload)
		if err != nil {
			return err
		}
		if !recoveryRecordMatchesTarget(current, target) {
			return ErrRecoveryMismatch
		}
		if current.USB != nil {
			if *current.USB != identity {
				return ErrRecoveryMismatch
			}
			result = current
			return nil
		}
		current.USB = &identity
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(current.EquipmentID), encoded); err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}

// RequestRelease is the durable distinction between a transient transport
// loss and an explicit operator disable. Until this bit is set, a captured
// modem remains reserved for the same binding and must not be returned to the
// endpoint host merely because Core or WSS is offline.
func (store *RecoveryStore) RequestRelease(target SourceTarget) (RecoveryRecord, error) {
	var result RecoveryRecord
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recoveryRecordsBucket)
		payload := bucket.Get([]byte(strings.TrimSpace(target.EquipmentID)))
		if payload == nil {
			return ErrRecoveryNotFound
		}
		current, err := decodeRecoveryRecord(payload)
		if err != nil {
			return err
		}
		if !recoveryRecordMatchesTarget(current, target) {
			return ErrRecoveryMismatch
		}
		if current.ReleaseRequested {
			result = current
			return nil
		}
		current.ReleaseRequested = true
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(current.EquipmentID), encoded); err != nil {
			return err
		}
		result = current
		return nil
	})
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
	if record.USB != nil {
		identity := *record.USB
		identity.StableID = strings.TrimSpace(identity.StableID)
		identity.BusID = strings.TrimSpace(identity.BusID)
		identity.Serial = strings.TrimSpace(identity.Serial)
		if err := validateRecoveryUSBIdentity(identity); err != nil || identity != *record.USB {
			return errors.New("invalid raw modem recovery record")
		}
	}
	return nil
}

func validateRecoveryUSBIdentity(identity RecoveryUSBIdentity) error {
	if strings.TrimSpace(identity.BusID) == "" || len(identity.BusID) > 128 ||
		len(identity.StableID) > 512 || len(identity.Serial) > 512 ||
		identity.VendorID == 0 || identity.ProductID == 0 ||
		strings.ContainsAny(identity.BusID, " \t\r\n") || strings.ContainsAny(identity.StableID, "\r\n") ||
		strings.ContainsAny(identity.Serial, "\r\n") {
		return errors.New("invalid raw modem recovery USB identity")
	}
	return nil
}

func sameRecoveryIdentity(left, right RecoveryRecord) bool {
	return left.SchemaVersion == right.SchemaVersion && left.SourceAgentID == right.SourceAgentID &&
		left.SourceProcessGeneration == right.SourceProcessGeneration && left.AttachmentID == right.AttachmentID &&
		left.SessionGeneration == right.SessionGeneration && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID && left.USBSessionID == right.USBSessionID &&
		left.ReleaseRequested == right.ReleaseRequested && sameRecoveryUSBIdentity(left.USB, right.USB)
}

func recoveryRecordMatchesTarget(record RecoveryRecord, target SourceTarget) bool {
	if record.SourceAgentID != target.SourceAgentID || record.AttachmentID != target.AttachmentID ||
		record.SessionGeneration != target.SessionGeneration || record.EquipmentID != target.EquipmentID ||
		record.CardID != target.CardID {
		return false
	}
	if target.Recovering {
		return record.USB != nil && !record.ReleaseRequested
	}
	return record.SourceProcessGeneration == target.SourceProcessGeneration && record.USBSessionID == target.USBSessionID
}

func sameRecoveryUSBIdentity(left, right *RecoveryUSBIdentity) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
