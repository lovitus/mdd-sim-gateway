package cellularmessages

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/boltsnapshot"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	bolt "go.etcd.io/bbolt"
)

const operationStoreSchema uint64 = 1

var ErrOperationConflict = errors.New("cellular SMS operation identity conflict")
var ErrUnresolvedOperation = errors.New("cellular SMS operation requires reconciliation before deletion")

type SubmissionHistory interface {
	Find(string, string, string) (providermessages.Record, bool, error)
}

var (
	operationMetadata = []byte("metadata")
	operationRecords  = []byte("operations")
	operationSchema   = []byte("schema_version")
	operationPurged   = []byte("purged_lines_v1")
)

type OperationRecord struct {
	DiagnosticCode    string    `json:"diagnostic_code,omitempty"`
	SchemaVersion     int       `json:"schema_version"`
	OperationID       string    `json:"operation_id"`
	MessageID         string    `json:"message_id"`
	LineID            string    `json:"line_id"`
	EquipmentID       string    `json:"equipment_id"`
	CardID            string    `json:"card_id"`
	AgentID           string    `json:"agent_id"`
	ProcessGeneration string    `json:"process_generation"`
	AttachmentID      string    `json:"attachment_id"`
	Recipient         string    `json:"recipient"`
	BodySHA256        string    `json:"body_sha256"`
	State             string    `json:"state"`
	References        []int     `json:"references,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type OperationStore struct{ db *bolt.DB }

func OpenOperationStore(path string, timeout time.Duration) (*OperationStore, error) {
	path = strings.TrimSpace(path)
	if path == "" || timeout <= 0 {
		return nil, errors.New("invalid cellular SMS operation store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	store := &OperationStore{db: db}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

func (store *OperationStore) initialize() error {
	return store.db.Update(func(tx *bolt.Tx) error {
		metadata, err := tx.CreateBucketIfNotExists(operationMetadata)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(operationRecords); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(operationPurged); err != nil {
			return err
		}
		var wire [8]byte
		binary.BigEndian.PutUint64(wire[:], operationStoreSchema)
		stored := metadata.Get(operationSchema)
		if stored == nil {
			return metadata.Put(operationSchema, wire[:])
		}
		if !bytes.Equal(stored, wire[:]) {
			return errors.New("unsupported cellular SMS operation store schema")
		}
		return nil
	})
}

func (store *OperationStore) Get(operationID string) (OperationRecord, bool, error) {
	var record OperationRecord
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		wire := tx.Bucket(operationRecords).Get([]byte(operationID))
		if wire == nil {
			return nil
		}
		if err := json.Unmarshal(wire, &record); err != nil {
			return errors.New("invalid persisted cellular SMS operation")
		}
		found = true
		return nil
	})
	return record, found, err
}

func (store *OperationStore) Begin(record OperationRecord) (OperationRecord, bool, error) {
	record.SchemaVersion = 1
	record.State = "prepared"
	record.References = nil
	record.CreatedAt = record.CreatedAt.UTC()
	var result OperationRecord
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(operationPurged).Get([]byte(record.LineID)) != nil {
			return errors.New("cellular SMS line was permanently deleted")
		}
		bucket := tx.Bucket(operationRecords)
		if prior := bucket.Get([]byte(record.OperationID)); prior != nil {
			if json.Unmarshal(prior, &result) != nil {
				return errors.New("invalid persisted cellular SMS operation")
			}
			if !sameOperation(result, record) {
				return ErrOperationConflict
			}
			return nil
		}
		wire, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(record.OperationID), wire); err != nil {
			return err
		}
		result, created = record, true
		return nil
	})
	return result, created, err
}

func (store *OperationStore) Mark(operationID, state string, references []int, diagnostic ...string) (OperationRecord, error) {
	var record OperationRecord
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationRecords)
		wire := bucket.Get([]byte(operationID))
		if wire == nil || json.Unmarshal(wire, &record) != nil {
			return errors.New("cellular SMS operation not found")
		}
		record.State = state
		if len(diagnostic) > 0 {
			record.DiagnosticCode = diagnostic[0]
		}
		record.References = append([]int(nil), references...)
		updated, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(operationID), updated)
	})
	return record, err
}

func (store *OperationStore) Delete(operationID string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(operationRecords).Delete([]byte(operationID))
	})
}

// The same write transaction either sees an earlier Begin or blocks every later
// Begin. No other database may be purged before this barrier is durable.
func (store *OperationStore) PrepareLineDeletion(lineID string, history SubmissionHistory) error {
	if strings.TrimSpace(lineID) == "" || history == nil {
		return errors.New("invalid cellular SMS deletion protection")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		barriers := tx.Bucket(operationPurged)
		if barriers.Get([]byte(lineID)) != nil {
			return nil
		}
		if err := tx.Bucket(operationRecords).ForEach(func(_, wire []byte) error {
			var record OperationRecord
			if json.Unmarshal(wire, &record) != nil {
				return errors.New("invalid persisted cellular SMS operation")
			}
			if record.LineID != lineID {
				return nil
			}
			if record.State != "submitted" || len(record.References) == 0 {
				return ErrUnresolvedOperation
			}
			for index, reference := range record.References {
				eventID := "cellular-submit-" + record.OperationID + "-" + stringID(index+1)
				message, found, err := history.Find(lineID, "cellular", eventID)
				if err != nil {
					return err
				}
				digest := sha256.Sum256([]byte(message.Body))
				if !found || message.Kind != providermessages.KindSubmitted || message.State != "submitted" ||
					message.MessageID != record.MessageID || message.Part != index+1 || message.RPMR != reference ||
					message.Recipient != record.Recipient || message.ProcessGeneration != record.ProcessGeneration ||
					message.CallID != messageReferenceID(reference) || hex.EncodeToString(digest[:]) != record.BodySHA256 {
					return ErrUnresolvedOperation
				}
			}
			return nil
		}); err != nil {
			return err
		}
		return barriers.Put([]byte(lineID), []byte{1})
	})
}

func (store *OperationStore) RetainLine(lineID string) error {
	return store.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(operationPurged).Get([]byte(lineID)) == nil {
			return errors.New("cellular SMS deletion barrier missing")
		}
		return nil
	})
}

func (store *OperationStore) Backup() ([]byte, error) { return boltsnapshot.Read(store.db) }

func (store *OperationStore) PurgeLine(lineID string) error {
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return errors.New("invalid cellular SMS purge line identity")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(operationPurged).Get([]byte(lineID)) == nil {
			return errors.New("cellular SMS deletion barrier missing")
		}
		bucket := tx.Bucket(operationRecords)
		var keys [][]byte
		if err := bucket.ForEach(func(key, value []byte) error {
			var record OperationRecord
			if json.Unmarshal(value, &record) != nil {
				return errors.New("invalid persisted cellular SMS operation")
			}
			if record.LineID == lineID {
				keys = append(keys, append([]byte(nil), key...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range keys {
			if err := bucket.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *OperationStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func sameOperation(left, right OperationRecord) bool {
	return left.SchemaVersion == right.SchemaVersion && left.OperationID == right.OperationID &&
		left.MessageID == right.MessageID && left.LineID == right.LineID && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID && left.AgentID == right.AgentID && left.ProcessGeneration == right.ProcessGeneration &&
		left.AttachmentID == right.AttachmentID && left.Recipient == right.Recipient && left.BodySHA256 == right.BodySHA256
}
