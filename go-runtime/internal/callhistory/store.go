// Package callhistory stores user-visible call records without owning call
// admission, recovery, media, or hangup policy.
package callhistory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	bolt "go.etcd.io/bbolt"
)

var recordsBucket = []byte("call-records-v1")

type Record struct {
	ID         string     `json:"id"`
	CallID     string     `json:"call_id"`
	LineID     string     `json:"line_id"`
	Transport  string     `json:"transport"`
	Direction  string     `json:"direction"`
	Peer       string     `json:"peer,omitempty"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
}

type Store struct {
	db        *bolt.DB
	closeOnce sync.Once
	closeErr  error
}

func Open(path string, timeout time.Duration) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" || timeout <= 0 {
		return nil, errors.New("invalid call history store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(recordsBucket)
		return err
	}); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{db: db}, nil
}

func (store *Store) Start(lineID, transport, callID, direction, peer string, at time.Time) error {
	if !validRecordIdentity(lineID, transport, callID, direction) || at.IsZero() || len(peer) > 512 {
		return errors.New("invalid call history start")
	}
	at = at.UTC()
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recordsBucket)
		key := recordKey(lineID, transport, callID)
		current, found, err := decodeRecord(bucket.Get(key))
		if err != nil {
			return err
		}
		if !found {
			status := "dialing"
			if direction == "in" {
				status = "ringing"
			}
			current = Record{ID: string(key), CallID: callID, LineID: lineID, Transport: transport,
				Direction: direction, Peer: strings.TrimSpace(peer), Status: status, StartedAt: at}
		} else {
			if current.EndedAt != nil {
				return nil
			}
			if current.Peer == "" {
				current.Peer = strings.TrimSpace(peer)
			}
			if current.Direction == "unknown" {
				current.Direction = direction
			}
		}
		return putRecord(bucket, key, current)
	})
}

func (store *Store) Active(lineID, transport, callID string, at time.Time) error {
	if at.IsZero() {
		return errors.New("invalid active call time")
	}
	return store.update(lineID, transport, callID, at, func(record *Record) {
		// A Provider-owned active-call snapshot is stronger evidence than an
		// earlier public operation failure or timeout. Reopen only this exact
		// call ID; no other history record participates in call admission.
		if record.EndedAt != nil {
			record.EndedAt = nil
		}
		answer := at.UTC()
		record.Status = "answered"
		if record.AnsweredAt == nil {
			record.AnsweredAt = &answer
		}
	})
}

func (store *Store) Finish(lineID, transport, callID, status string, at time.Time) error {
	if !terminalStatus(status) || at.IsZero() {
		return errors.New("invalid call history terminal state")
	}
	return store.update(lineID, transport, callID, at, func(record *Record) {
		if record.EndedAt == nil {
			ended := at.UTC()
			record.Status, record.EndedAt = status, &ended
		}
	})
}

func (store *Store) update(lineID, transport, callID string, at time.Time, mutate func(*Record)) error {
	if !validRecordIdentity(lineID, transport, callID, "unknown") || at.IsZero() || mutate == nil {
		return errors.New("invalid call history identity")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recordsBucket)
		key := recordKey(lineID, transport, callID)
		record, found, err := decodeRecord(bucket.Get(key))
		if err != nil {
			return err
		}
		if !found {
			record = Record{ID: string(key), CallID: callID, LineID: lineID, Transport: transport,
				Direction: "unknown", Status: "dialing", StartedAt: at.UTC()}
		}
		mutate(&record)
		return putRecord(bucket, key, record)
	})
}

// ObserveVoWiFiSnapshot converts the Provider's exact active/pending call
// identities into history only. It never changes the runtime or call state.
func (store *Store) ObserveVoWiFiSnapshot(snapshot vowifiipc.Snapshot, at time.Time) error {
	if snapshot.Validate() != nil || at.IsZero() {
		return errors.New("invalid VoWiFi call snapshot")
	}
	if pending := snapshot.PendingIncomingCall; pending != nil {
		if err := store.Start(snapshot.LineID, "vowifi", pending.CallID, "in", pending.Caller, pending.ReceivedAt); err != nil {
			return err
		}
		return store.finishOpenExcept(snapshot.LineID, "vowifi", pending.CallID, "interrupted", at)
	}
	if active := snapshot.ActiveCall; active != nil {
		if err := store.Start(snapshot.LineID, "vowifi", active.CallID, "unknown", "", at); err != nil {
			return err
		}
		if err := store.finishOpenExcept(snapshot.LineID, "vowifi", active.CallID, "interrupted", at); err != nil {
			return err
		}
		return store.Active(snapshot.LineID, "vowifi", active.CallID, at)
	}
	return store.finishOpen(snapshot.LineID, "vowifi", func(record Record) string {
		// Outgoing call setup owns its explicit success/failure result. An
		// unrelated status heartbeat with no active call must not race it.
		if record.Status == "dialing" {
			return ""
		}
		if record.Status == "ringing" || record.Direction == "in" && record.AnsweredAt == nil {
			return "missed"
		}
		return "ended"
	}, at)
}

func (store *Store) finishOpenExcept(lineID, transport, keepCallID, status string, at time.Time) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recordsBucket)
		return bucket.ForEach(func(key, value []byte) error {
			record, found, err := decodeRecord(value)
			if err != nil || !found {
				return err
			}
			if record.LineID != lineID || record.Transport != transport || record.CallID == keepCallID || record.EndedAt != nil {
				return nil
			}
			ended := at.UTC()
			record.Status, record.EndedAt = status, &ended
			return putRecord(bucket, key, record)
		})
	})
}

func (store *Store) finishOpen(lineID, transport string, status func(Record) string, at time.Time) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recordsBucket)
		return bucket.ForEach(func(key, value []byte) error {
			record, found, err := decodeRecord(value)
			if err != nil || !found {
				return err
			}
			if record.LineID != lineID || record.Transport != transport || record.EndedAt != nil {
				return nil
			}
			terminal := status(record)
			if terminal == "" {
				return nil
			}
			ended := at.UTC()
			record.Status, record.EndedAt = terminal, &ended
			return putRecord(bucket, key, record)
		})
	})
}

func (store *Store) List(lineID string, limit int) ([]Record, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("call history limit must be between 1 and 500")
	}
	result := []Record{}
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(recordsBucket).ForEach(func(_, value []byte) error {
			record, found, err := decodeRecord(value)
			if err != nil || !found {
				return err
			}
			if lineID == "" || record.LineID == lineID {
				result = append(result, record)
			}
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, err
}

func (store *Store) Delete(ids []string) (int, error) {
	if len(ids) < 1 || len(ids) > 500 {
		return 0, errors.New("invalid call history deletion")
	}
	deleted := 0
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recordsBucket)
		for _, id := range ids {
			if id == "" || len(id) > 512 {
				return errors.New("invalid call history ID")
			}
			record, found, err := decodeRecord(bucket.Get([]byte(id)))
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if record.EndedAt == nil {
				return errors.New("active call history cannot be deleted")
			}
			if err := bucket.Delete([]byte(id)); err != nil {
				return err
			}
			deleted++
		}
		return nil
	})
	return deleted, err
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.closeOnce.Do(func() { store.closeErr = store.db.Close() })
	return store.closeErr
}

func recordKey(lineID, transport, callID string) []byte {
	digest := sha256.Sum256([]byte(lineID + "\x00" + transport + "\x00" + callID))
	return []byte(hex.EncodeToString(digest[:16]))
}

func validRecordIdentity(lineID, transport, callID, direction string) bool {
	return strings.TrimSpace(lineID) != "" && strings.TrimSpace(callID) != "" &&
		(transport == "vowifi" || transport == "cellular") &&
		(direction == "in" || direction == "out" || direction == "unknown")
}

func terminalStatus(status string) bool {
	return status == "ended" || status == "failed" || status == "missed" || status == "rejected" || status == "interrupted"
}

func decodeRecord(value []byte) (Record, bool, error) {
	if value == nil {
		return Record{}, false, nil
	}
	var record Record
	if err := json.Unmarshal(value, &record); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

func putRecord(bucket *bolt.Bucket, key []byte, record Record) error {
	wire, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return bucket.Put(key, wire)
}
