package agentsim

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	bolt "go.etcd.io/bbolt"
)

const downloadStoreSchema uint64 = 1

var (
	downloadMetadataBucket = []byte("metadata")
	downloadRecordsBucket  = []byte("downloads")
	downloadSchemaKey      = []byte("schema_version")
	ErrDownloadConflict    = errors.New("eUICC download operation identity conflict")
)

type DownloadRecord struct {
	SchemaVersion uint64                     `json:"schema_version"`
	OperationID   string                     `json:"operation_id"`
	EID           string                     `json:"eid"`
	Job           agentlink.EUICCDownloadJob `json:"job"`
}

type DownloadStore struct{ db *bolt.DB }

func OpenDownloadStore(path string, timeout time.Duration) (*DownloadStore, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid eUICC download store configuration")
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
	store := &DownloadStore{db: db}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

func (store *DownloadStore) initialize() error {
	return store.db.Update(func(tx *bolt.Tx) error {
		metadata, err := tx.CreateBucketIfNotExists(downloadMetadataBucket)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(downloadRecordsBucket); err != nil {
			return err
		}
		var wire [8]byte
		binary.BigEndian.PutUint64(wire[:], downloadStoreSchema)
		stored := metadata.Get(downloadSchemaKey)
		if stored == nil {
			return metadata.Put(downloadSchemaKey, wire[:])
		}
		if !bytes.Equal(stored, wire[:]) {
			return errors.New("unsupported eUICC download store schema")
		}
		return nil
	})
}

func (store *DownloadStore) Begin(record DownloadRecord) (DownloadRecord, bool, error) {
	record.SchemaVersion = downloadStoreSchema
	if err := validateDownloadRecord(record); err != nil {
		return DownloadRecord{}, false, err
	}
	var result DownloadRecord
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(downloadRecordsBucket)
		key := []byte(record.OperationID)
		if prior := bucket.Get(key); prior != nil {
			if json.Unmarshal(prior, &result) != nil || validateDownloadRecord(result) != nil {
				return errors.New("invalid persisted eUICC download")
			}
			if result.EID != record.EID {
				return ErrDownloadConflict
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

func (store *DownloadStore) Get(operationID string) (DownloadRecord, bool, error) {
	var result DownloadRecord
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		wire := tx.Bucket(downloadRecordsBucket).Get([]byte(operationID))
		if wire == nil {
			return nil
		}
		if json.Unmarshal(wire, &result) != nil || validateDownloadRecord(result) != nil {
			return errors.New("invalid persisted eUICC download")
		}
		found = true
		return nil
	})
	return result, found, err
}

func (store *DownloadStore) Mark(operationID string, job agentlink.EUICCDownloadJob) (DownloadRecord, error) {
	if err := job.Validate(); err != nil {
		return DownloadRecord{}, err
	}
	var result DownloadRecord
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(downloadRecordsBucket)
		wire := bucket.Get([]byte(operationID))
		if wire == nil || json.Unmarshal(wire, &result) != nil || validateDownloadRecord(result) != nil {
			return errors.New("eUICC download operation not found")
		}
		result.Job = cloneDownloadJob(job)
		updated, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(operationID), updated)
	})
	return result, err
}

func (store *DownloadStore) RecoverInterrupted(now time.Time) error {
	now = now.UTC()
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(downloadRecordsBucket)
		return bucket.ForEach(func(key, wire []byte) error {
			var record DownloadRecord
			if json.Unmarshal(wire, &record) != nil || validateDownloadRecord(record) != nil {
				return errors.New("invalid persisted eUICC download")
			}
			switch record.Job.State {
			case agentlink.EUICCDownloadQueued, agentlink.EUICCDownloadRunning, agentlink.EUICCDownloadCancelling:
				record.Job.State = agentlink.EUICCDownloadUncertain
				record.Job.Code = "agent_runtime_interrupted"
				record.Job.UpdatedAt = now
				updated, err := json.Marshal(record)
				if err != nil {
					return err
				}
				return bucket.Put(key, updated)
			default:
				return nil
			}
		})
	})
}

func (store *DownloadStore) Close() error { return store.db.Close() }

func validateDownloadRecord(record DownloadRecord) error {
	status := agentlink.EUICCDownloadCommand{
		OperationID: record.OperationID, EID: record.EID, Action: agentlink.EUICCDownloadStatus,
	}
	if record.SchemaVersion != downloadStoreSchema || status.Validate() != nil ||
		record.Job.Validate() != nil {
		return errors.New("invalid eUICC download record")
	}
	return nil
}

func cloneDownloadJob(source agentlink.EUICCDownloadJob) agentlink.EUICCDownloadJob {
	copy := source
	if source.Metadata != nil {
		metadata := *source.Metadata
		copy.Metadata = &metadata
	}
	return copy
}
