package linecatalog

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

type DeletionStage string

const (
	DeletionPrepared      DeletionStage = "prepared"
	DeletionNotifications DeletionStage = "notifications_deleted"
	DeletionEvents        DeletionStage = "events_deleted"
	DeletionAllowance     DeletionStage = "allowance_deleted"
	DeletionMessages      DeletionStage = "messages_finalized"
	DeletionSMSOperations DeletionStage = "sms_operations_deleted"
	DeletionCalls         DeletionStage = "calls_finalized"
	DeletionSucceeded     DeletionStage = "succeeded"
)

var (
	ErrLineNotDeleted   = errors.New("line is not in the recycle bin")
	ErrDeletionConflict = errors.New("line deletion operation conflicts with durable state")
	ErrDeletionStage    = errors.New("line deletion stage changed")
)

type DeletionReceipt struct {
	SchemaVersion   int           `json:"schema_version"`
	OperationID     string        `json:"operation_id"`
	LineID          string        `json:"line_id"`
	DeleteHistory   bool          `json:"delete_history"`
	Stage           DeletionStage `json:"stage"`
	CatalogRevision uint64        `json:"catalog_revision"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

func (store *Store) GetDeletion(operationID string) (DeletionReceipt, bool, error) {
	operationID = strings.TrimSpace(operationID)
	if !validOperationID(operationID) {
		return DeletionReceipt{}, false, errors.New("invalid line deletion operation ID")
	}
	var receipt DeletionReceipt
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		wire := tx.Bucket(deletionOperationBucket).Get([]byte(operationID))
		if wire == nil {
			return nil
		}
		if json.Unmarshal(wire, &receipt) != nil || receipt.Validate() != nil {
			return errors.New("stored line deletion receipt is corrupt")
		}
		found = true
		return nil
	})
	return receipt, found, err
}

func (receipt DeletionReceipt) Validate() error {
	if receipt.SchemaVersion != 1 || !validOperationID(receipt.OperationID) ||
		!validIdentifier(receipt.LineID) || receipt.CatalogRevision == 0 ||
		receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() || receipt.UpdatedAt.Before(receipt.CreatedAt) {
		return errors.New("invalid line deletion receipt")
	}
	switch receipt.Stage {
	case DeletionPrepared, DeletionNotifications, DeletionEvents, DeletionAllowance, DeletionMessages,
		DeletionSMSOperations, DeletionCalls, DeletionSucceeded:
		return nil
	default:
		return errors.New("invalid line deletion stage")
	}
}

func (store *Store) PrepareDeletionExpected(lineID, operationID string, deleteHistory bool, expectedRevision uint64, now time.Time) (DeletionReceipt, bool, error) {
	lineID, operationID, now = strings.TrimSpace(lineID), strings.TrimSpace(operationID), now.UTC()
	if !validIdentifier(lineID) || !validOperationID(operationID) || expectedRevision == 0 || now.IsZero() {
		return DeletionReceipt{}, false, errors.New("invalid line deletion request")
	}
	var receipt DeletionReceipt
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		operations := tx.Bucket(deletionOperationBucket)
		if wire := operations.Get([]byte(operationID)); wire != nil {
			if json.Unmarshal(wire, &receipt) != nil || receipt.Validate() != nil {
				return errors.New("stored line deletion receipt is corrupt")
			}
			if receipt.LineID != lineID || receipt.DeleteHistory != deleteHistory {
				return ErrDeletionConflict
			}
			return nil
		}
		revision := bytesUint64(tx.Bucket(metadataBucket).Get(revisionKey))
		if revision != expectedRevision {
			receipt.CatalogRevision = revision
			return ErrRevision
		}
		wire := tx.Bucket(linesBucket).Get([]byte(lineID))
		if wire == nil {
			return ErrNotFound
		}
		var line Line
		if json.Unmarshal(wire, &line) != nil || line.normalizeAndValidate() != nil {
			return errors.New("stored line is corrupt")
		}
		if tx.Bucket(lifecycleBucket).Get([]byte(lineID)) == nil {
			return ErrLineNotDeleted
		}
		if line.Enabled || bytesEqualOne(tx.Bucket(runtimeIntentsBucket).Get([]byte(lineID))) {
			return ErrLineActive
		}
		if raw := tx.Bucket(rawModemBindingsBucket).Get([]byte(lineID)); raw != nil {
			var binding RawModemBinding
			if json.Unmarshal(raw, &binding) != nil || binding.normalizeAndValidate() != nil || binding.Epoch == 0 {
				return errors.New("stored raw modem binding is corrupt")
			}
			if binding.Enabled {
				return ErrLineActive
			}
		}
		active, err := activeProvisionOperation(tx.Bucket(operationBucket), lineID, "")
		if err != nil {
			return err
		}
		if active {
			return ErrLineOperationActive
		}
		conflict := false
		if err := operations.ForEach(func(_, value []byte) error {
			var prior DeletionReceipt
			if json.Unmarshal(value, &prior) != nil || prior.Validate() != nil {
				return errors.New("stored line deletion receipt is corrupt")
			}
			if prior.LineID == lineID && prior.Stage != DeletionSucceeded {
				conflict = true
				receipt = prior
			}
			return nil
		}); err != nil {
			return err
		}
		if conflict {
			return ErrDeletionConflict
		}
		receipt = DeletionReceipt{SchemaVersion: 1, OperationID: operationID, LineID: lineID,
			DeleteHistory: deleteHistory, Stage: DeletionPrepared, CatalogRevision: revision, CreatedAt: now, UpdatedAt: now}
		encoded, _ := json.Marshal(receipt)
		created = true
		return operations.Put([]byte(operationID), encoded)
	})
	return receipt, created, err
}

func (store *Store) AdvanceDeletion(operationID string, expected, next DeletionStage, now time.Time) (DeletionReceipt, error) {
	operationID, now = strings.TrimSpace(operationID), now.UTC()
	if !validOperationID(operationID) || now.IsZero() {
		return DeletionReceipt{}, errors.New("invalid line deletion advancement")
	}
	var receipt DeletionReceipt
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(deletionOperationBucket)
		wire := bucket.Get([]byte(operationID))
		if wire == nil || json.Unmarshal(wire, &receipt) != nil || receipt.Validate() != nil {
			return ErrNotFound
		}
		if receipt.Stage == next || receipt.Stage == DeletionSucceeded {
			return nil
		}
		if receipt.Stage != expected {
			return ErrDeletionStage
		}
		receipt.Stage, receipt.UpdatedAt = next, now
		encoded, _ := json.Marshal(receipt)
		return bucket.Put([]byte(operationID), encoded)
	})
	return receipt, err
}

func (store *Store) FinalizeDeletion(operationID string, now time.Time) (DeletionReceipt, error) {
	operationID, now = strings.TrimSpace(operationID), now.UTC()
	if !validOperationID(operationID) || now.IsZero() {
		return DeletionReceipt{}, errors.New("invalid line deletion finalization")
	}
	var receipt DeletionReceipt
	err := store.db.Update(func(tx *bolt.Tx) error {
		deletions := tx.Bucket(deletionOperationBucket)
		wire := deletions.Get([]byte(operationID))
		if wire == nil || json.Unmarshal(wire, &receipt) != nil || receipt.Validate() != nil {
			return ErrNotFound
		}
		if receipt.Stage == DeletionSucceeded {
			return nil
		}
		if receipt.Stage != DeletionCalls {
			return ErrDeletionStage
		}
		lineWire := tx.Bucket(linesBucket).Get([]byte(receipt.LineID))
		if lineWire == nil {
			return ErrDeletionConflict
		}
		var line Line
		if json.Unmarshal(lineWire, &line) != nil || line.normalizeAndValidate() != nil {
			return errors.New("stored line is corrupt")
		}
		if tx.Bucket(lifecycleBucket).Get([]byte(line.ID)) == nil || line.Enabled ||
			bytesEqualOne(tx.Bucket(runtimeIntentsBucket).Get([]byte(line.ID))) {
			return ErrLineActive
		}
		if raw := tx.Bucket(rawModemBindingsBucket).Get([]byte(line.ID)); raw != nil {
			var binding RawModemBinding
			if json.Unmarshal(raw, &binding) != nil || binding.normalizeAndValidate() != nil || binding.Enabled {
				return ErrLineActive
			}
			if err := tx.Bucket(rawModemBindingsBucket).Delete([]byte(line.ID)); err != nil {
				return err
			}
			rawRevision := bytesUint64(tx.Bucket(metadataBucket).Get(rawModemRevisionKey)) + 1
			if err := tx.Bucket(metadataBucket).Put(rawModemRevisionKey, uint64Bytes(rawRevision)); err != nil {
				return err
			}
		}
		for _, bucket := range []*bolt.Bucket{tx.Bucket(linesBucket), tx.Bucket(lifecycleBucket), tx.Bucket(runtimeIntentsBucket)} {
			if err := bucket.Delete([]byte(line.ID)); err != nil {
				return err
			}
		}
		if owner := tx.Bucket(cardsBucket).Get([]byte(line.CardID)); owner == nil || string(owner) != line.ID {
			return ErrDeletionConflict
		}
		if err := tx.Bucket(cardsBucket).Delete([]byte(line.CardID)); err != nil {
			return err
		}
		var provisionKeys [][]byte
		if err := tx.Bucket(operationBucket).ForEach(func(key, value []byte) error {
			var operation OperationReceipt
			if json.Unmarshal(value, &operation) != nil || operation.Validate() != nil {
				return errors.New("stored operation receipt is corrupt")
			}
			if operation.LineID == line.ID {
				provisionKeys = append(provisionKeys, append([]byte(nil), key...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range provisionKeys {
			if err := tx.Bucket(operationBucket).Delete(key); err != nil {
				return err
			}
		}
		revision := bytesUint64(tx.Bucket(metadataBucket).Get(revisionKey)) + 1
		if err := tx.Bucket(metadataBucket).Put(revisionKey, uint64Bytes(revision)); err != nil {
			return err
		}
		receipt.Stage, receipt.CatalogRevision, receipt.UpdatedAt = DeletionSucceeded, revision, now
		encoded, _ := json.Marshal(receipt)
		return deletions.Put([]byte(operationID), encoded)
	})
	return receipt, err
}

func bytesEqualOne(value []byte) bool { return len(value) == 1 && value[0] == 1 }

func deletedLineIdentity(bucket *bolt.Bucket, lineID string) (bool, error) {
	found := false
	err := bucket.ForEach(func(_, value []byte) error {
		var receipt DeletionReceipt
		if json.Unmarshal(value, &receipt) != nil || receipt.Validate() != nil {
			return errors.New("stored line deletion receipt is corrupt")
		}
		if receipt.LineID == lineID && receipt.Stage == DeletionSucceeded {
			found = true
		}
		return nil
	})
	return found, err
}

func activeDeletionOperation(bucket *bolt.Bucket, lineID string) (bool, error) {
	active := false
	err := bucket.ForEach(func(_, value []byte) error {
		var receipt DeletionReceipt
		if json.Unmarshal(value, &receipt) != nil || receipt.Validate() != nil {
			return errors.New("stored line deletion receipt is corrupt")
		}
		if receipt.LineID == lineID && receipt.Stage != DeletionSucceeded {
			active = true
		}
		return nil
	})
	return active, err
}
