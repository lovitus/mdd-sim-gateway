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

var (
	recordsBucket                    = []byte("call-records-v1")
	notificationSourceBucket         = []byte("notification-source-outbox-v1")
	cellularNotificationSourceBucket = []byte("cellular-notification-source-outbox-v1")
	cellularSourceBucket             = []byte("cellular-call-source-v1")
	purgedLinesBucket                = []byte("purged-lines-v1")
)

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

const NotificationSourceSchemaVersion = 1

type NotificationSource struct {
	SchemaVersion int       `json:"schema_version"`
	SourceID      string    `json:"source_id"`
	LineID        string    `json:"line_id"`
	CardID        string    `json:"card_id,omitempty"`
	Transport     string    `json:"transport"`
	Peer          string    `json:"peer,omitempty"`
	ReceivedAt    time.Time `json:"received_at"`
	NotBefore     time.Time `json:"not_before"`
	Acked         bool      `json:"acked,omitempty"`
}

type CellularCallSource struct {
	IncomingEventID      string    `json:"incoming_event_id"`
	LastEventID          string    `json:"last_event_id"`
	Revision             uint64    `json:"revision"`
	LineID               string    `json:"line_id"`
	AgentID              string    `json:"agent_id"`
	ProcessGeneration    string    `json:"process_generation"`
	AttachmentID         string    `json:"attachment_id"`
	EquipmentID          string    `json:"equipment_id"`
	CardID               string    `json:"card_id"`
	SIMSessionGeneration string    `json:"sim_session_generation"`
	Occurrence           uint64    `json:"occurrence"`
	NativeCallIndex      int       `json:"native_call_index"`
	State                string    `json:"state"`
	Direction            string    `json:"direction"`
	Number               string    `json:"number,omitempty"`
	FirstObservedAt      time.Time `json:"first_observed_at"`
	ObservedAt           time.Time `json:"observed_at"`
	ReceivedAt           time.Time `json:"received_at"`
	Notify               bool      `json:"notify"`
}

func (source CellularCallSource) validate() error {
	if !validRecordIdentity(source.LineID, "cellular", source.IncomingEventID, "in") ||
		strings.TrimSpace(source.LastEventID) == "" || source.Revision == 0 ||
		strings.TrimSpace(source.AgentID) == "" || strings.TrimSpace(source.ProcessGeneration) == "" ||
		strings.TrimSpace(source.AttachmentID) == "" || strings.TrimSpace(source.EquipmentID) == "" ||
		!validCardID(source.CardID) || strings.TrimSpace(source.SIMSessionGeneration) == "" ||
		source.Occurrence == 0 || source.NativeCallIndex < 1 || source.NativeCallIndex > 255 ||
		!oneOfCallState(source.State) || source.Direction != "in" || len(source.Number) > 64 ||
		source.FirstObservedAt.IsZero() || source.ObservedAt.IsZero() || source.ReceivedAt.IsZero() ||
		source.Notify && source.State != "ringing_in" {
		return errors.New("invalid cellular call source")
	}
	return nil
}

func (source NotificationSource) validate() error {
	if source.SchemaVersion != NotificationSourceSchemaVersion ||
		!validRecordIdentity(source.LineID, source.Transport, source.SourceID, "in") ||
		(source.Transport != "vowifi" && source.Transport != "cellular") || source.ReceivedAt.IsZero() || source.NotBefore.IsZero() ||
		source.NotBefore.Before(source.ReceivedAt) || len(source.Peer) > 512 ||
		(!source.Acked && !validCardID(source.CardID)) || (source.Acked && source.CardID != "") {
		return errors.New("invalid call notification source")
	}
	return nil
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
		for _, bucket := range [][]byte{recordsBucket, notificationSourceBucket, cellularNotificationSourceBucket, cellularSourceBucket, purgedLinesBucket} {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{db: db}, nil
}

func (store *Store) Start(lineID, transport, callID, direction, peer string, at time.Time) error {
	return store.start(lineID, transport, callID, direction, peer, at, "", time.Time{})
}

func (store *Store) start(lineID, transport, callID, direction, peer string, at time.Time,
	notificationCardID string, notificationReceivedAt time.Time,
) error {
	if !validRecordIdentity(lineID, transport, callID, direction) || at.IsZero() || len(peer) > 512 {
		return errors.New("invalid call history start")
	}
	at, notificationReceivedAt = at.UTC(), notificationReceivedAt.UTC()
	return store.db.Update(func(tx *bolt.Tx) error {
		return startTx(tx, lineID, transport, callID, direction, peer, at,
			notificationCardID, notificationReceivedAt)
	})
}

func startTx(tx *bolt.Tx, lineID, transport, callID, direction, peer string, at time.Time,
	notificationCardID string, notificationReceivedAt time.Time,
) error {
	if tx.Bucket(purgedLinesBucket).Get([]byte(lineID)) != nil {
		return errors.New("call line was permanently deleted")
	}
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
	if err := putRecord(bucket, key, current); err != nil {
		return err
	}
	// Only a real Provider/Agent event supplies notificationReceivedAt.
	// Browser preparation never calls this notification path.
	if direction != "in" || (transport != "vowifi" && transport != "cellular") ||
		notificationReceivedAt.IsZero() || !validCardID(notificationCardID) {
		return nil
	}
	sources := tx.Bucket(notificationSourceBucket)
	if transport == "cellular" {
		sources = tx.Bucket(cellularNotificationSourceBucket)
	}
	sourceID := transport + "-call-" + current.ID
	wire := sources.Get([]byte(sourceID))
	if wire == nil {
		source := NotificationSource{
			SchemaVersion: NotificationSourceSchemaVersion,
			SourceID:      sourceID, LineID: lineID, CardID: notificationCardID, Transport: transport,
			Peer: strings.TrimSpace(peer), ReceivedAt: notificationReceivedAt,
			NotBefore: notificationReceivedAt.Add(4 * time.Second),
		}
		if source.validate() != nil {
			return errors.New("invalid call notification source")
		}
		wire, err := json.Marshal(source)
		if err != nil {
			return err
		}
		return sources.Put([]byte(sourceID), wire)
	}
	if strings.TrimSpace(peer) == "" {
		return nil
	}
	var source NotificationSource
	if json.Unmarshal(wire, &source) != nil || source.validate() != nil {
		return errors.New("stored call notification source is invalid")
	}
	if source.Acked || source.Peer != "" {
		return nil
	}
	source.Peer = strings.TrimSpace(peer)
	encoded, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return sources.Put([]byte(sourceID), encoded)
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
		return updateRecordTx(tx, lineID, transport, callID, at.UTC(), mutate)
	})
}

func updateRecordTx(tx *bolt.Tx, lineID, transport, callID string, at time.Time, mutate func(*Record)) error {
	if tx.Bucket(purgedLinesBucket).Get([]byte(lineID)) != nil {
		return errors.New("call line was permanently deleted")
	}
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
}

// ObserveVoWiFiSnapshot converts the Provider's exact active/pending call
// identities into history only. It never changes the runtime or call state.
func (store *Store) ObserveVoWiFiSnapshot(snapshot vowifiipc.Snapshot, cardID string, at time.Time) error {
	cardID = strings.TrimSpace(cardID)
	if snapshot.Validate() != nil || at.IsZero() {
		return errors.New("invalid VoWiFi call snapshot")
	}
	if pending := snapshot.PendingIncomingCall; pending != nil {
		if err := store.start(snapshot.LineID, "vowifi", pending.CallID, "in", pending.Caller,
			pending.ReceivedAt, cardID, pending.ReceivedAt); err != nil {
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

// AcceptCellularEvent commits the current Agent call source and user-visible
// history in one bbolt transaction. Agent revisions are monotonic per
// incoming occurrence; duplicate delivery is inert and an older retry cannot
// reopen a newer state.
func (store *Store) AcceptCellularEvent(source CellularCallSource) (bool, error) {
	if err := source.validate(); err != nil {
		return false, err
	}
	source.FirstObservedAt = source.FirstObservedAt.UTC()
	source.ObservedAt = source.ObservedAt.UTC()
	source.ReceivedAt = source.ReceivedAt.UTC()
	stored := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(purgedLinesBucket).Get([]byte(source.LineID)) != nil {
			return errors.New("call line was permanently deleted")
		}
		bucket := tx.Bucket(cellularSourceBucket)
		var prior CellularCallSource
		suppressRingingRegression := false
		if wire := bucket.Get([]byte(source.IncomingEventID)); wire != nil {
			if json.Unmarshal(wire, &prior) != nil || prior.validate() != nil {
				return errors.New("stored cellular call source is invalid")
			}
			if prior.LineID != source.LineID || prior.AgentID != source.AgentID ||
				prior.EquipmentID != source.EquipmentID || prior.CardID != source.CardID ||
				prior.Occurrence != source.Occurrence || prior.NativeCallIndex != source.NativeCallIndex {
				return errors.New("cellular call source identity conflict")
			}
			if source.Revision < prior.Revision {
				return nil
			}
			if source.Revision == prior.Revision {
				if source.LastEventID != prior.LastEventID {
					return errors.New("cellular call revision conflict")
				}
				if prior.State == source.State && source.State != "idle" && source.State != "unavailable" &&
					sameCellularCallBusiness(prior, source) {
					prior.ProcessGeneration = source.ProcessGeneration
					prior.AttachmentID = source.AttachmentID
					prior.SIMSessionGeneration = source.SIMSessionGeneration
					prior.ReceivedAt = source.ReceivedAt
					wire, err := json.Marshal(prior)
					if err != nil {
						return err
					}
					if err := bucket.Put([]byte(prior.IncomingEventID), wire); err != nil {
						return err
					}
					stored = true
				}
				return nil
			}
			if source.State == "ringing_in" && (prior.State == "active" || prior.State == "idle") {
				source.State, source.Notify = prior.State, false
				source.Number = prior.Number
				suppressRingingRegression = true
			}
		}
		if !suppressRingingRegression {
			switch source.State {
			case "ringing_in":
				notificationAt := time.Time{}
				if source.Notify {
					notificationAt = source.ReceivedAt
				}
				if err := startTx(tx, source.LineID, "cellular", source.IncomingEventID, "in", source.Number,
					source.FirstObservedAt, source.CardID, notificationAt); err != nil {
					return err
				}
			case "active", "held":
				if err := startTx(tx, source.LineID, "cellular", source.IncomingEventID, "in", source.Number,
					source.FirstObservedAt, "", time.Time{}); err != nil {
					return err
				}
				if err := updateRecordTx(tx, source.LineID, "cellular", source.IncomingEventID, source.ObservedAt,
					func(record *Record) {
						if record.EndedAt != nil {
							record.EndedAt = nil
						}
						answer := source.ObservedAt
						record.Status = "answered"
						if record.AnsweredAt == nil {
							record.AnsweredAt = &answer
						}
					}); err != nil {
					return err
				}
			case "idle", "unavailable":
				if prior.IncomingEventID != "" {
					if err := updateRecordTx(tx, source.LineID, "cellular", source.IncomingEventID, source.ObservedAt,
						func(record *Record) {
							if record.EndedAt != nil {
								return
							}
							ended := source.ObservedAt
							if source.State == "unavailable" {
								record.Status = "interrupted"
							} else if record.Status == "ringing" && record.AnsweredAt == nil {
								record.Status = "missed"
							} else {
								record.Status = "ended"
							}
							record.EndedAt = &ended
						}); err != nil {
						return err
					}
				}
			}
		}
		wire, err := json.Marshal(source)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(source.IncomingEventID), wire); err != nil {
			return err
		}
		stored = true
		return nil
	})
	return stored, err
}

func sameCellularCallBusiness(left, right CellularCallSource) bool {
	return left.IncomingEventID == right.IncomingEventID && left.LastEventID == right.LastEventID &&
		left.Revision == right.Revision && left.LineID == right.LineID && left.AgentID == right.AgentID &&
		left.EquipmentID == right.EquipmentID && left.CardID == right.CardID && left.Occurrence == right.Occurrence &&
		left.NativeCallIndex == right.NativeCallIndex && left.State == right.State && left.Direction == right.Direction &&
		left.Number == right.Number && left.FirstObservedAt.Equal(right.FirstObservedAt) &&
		left.ObservedAt.Equal(right.ObservedAt) && left.Notify == right.Notify
}

func (store *Store) CellularCall(incomingEventID string) (CellularCallSource, bool, error) {
	var source CellularCallSource
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		wire := tx.Bucket(cellularSourceBucket).Get([]byte(strings.TrimSpace(incomingEventID)))
		if wire == nil {
			return nil
		}
		if json.Unmarshal(wire, &source) != nil || source.validate() != nil {
			return errors.New("stored cellular call source is invalid")
		}
		found = true
		return nil
	})
	return source, found, err
}

func (store *Store) CurrentCellularCalls() ([]CellularCallSource, error) {
	result := []CellularCallSource{}
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(cellularSourceBucket).ForEach(func(_, wire []byte) error {
			var source CellularCallSource
			if json.Unmarshal(wire, &source) != nil || source.validate() != nil {
				return errors.New("stored cellular call source is invalid")
			}
			if source.State != "idle" && source.State != "unavailable" {
				result = append(result, source)
			}
			return nil
		})
	})
	sort.Slice(result, func(left, right int) bool {
		return result[left].FirstObservedAt.Before(result[right].FirstObservedAt)
	})
	return result, err
}

func (store *Store) MarkCellularAnswered(incomingEventID string, at time.Time) error {
	return store.markCellularAction(incomingEventID, "active", "answered", at)
}

func (store *Store) MarkCellularRejected(incomingEventID string, at time.Time) error {
	return store.markCellularAction(incomingEventID, "idle", "rejected", at)
}

func (store *Store) MarkCellularEnded(incomingEventID string, at time.Time) error {
	return store.markCellularAction(incomingEventID, "idle", "ended", at)
}

func (store *Store) markCellularAction(incomingEventID, sourceState, historyState string, at time.Time) error {
	if at.IsZero() {
		return errors.New("invalid cellular call action time")
	}
	at = at.UTC()
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(cellularSourceBucket)
		wire := bucket.Get([]byte(strings.TrimSpace(incomingEventID)))
		if wire == nil {
			return errors.New("cellular incoming call not found")
		}
		var source CellularCallSource
		if json.Unmarshal(wire, &source) != nil || source.validate() != nil ||
			source.State != "ringing_in" && !(historyState == "ended" && (source.State == "active" || source.State == "held")) {
			return errors.New("cellular incoming call is no longer ringing")
		}
		source.State, source.Notify, source.ObservedAt, source.ReceivedAt = sourceState, false, at, at
		encoded, err := json.Marshal(source)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(source.IncomingEventID), encoded); err != nil {
			return err
		}
		return updateRecordTx(tx, source.LineID, "cellular", source.IncomingEventID, at, func(record *Record) {
			if historyState == "answered" {
				answer := at
				record.Status = "answered"
				if record.AnsweredAt == nil {
					record.AnsweredAt = &answer
				}
				return
			}
			if record.EndedAt == nil {
				ended := at
				record.Status, record.EndedAt = historyState, &ended
			}
		})
	})
}

func (store *Store) PendingNotificationSources(now time.Time, limit int) ([]NotificationSource, error) {
	now = now.UTC()
	if now.IsZero() || limit < 1 || limit > 500 {
		return nil, errors.New("invalid call notification source query")
	}
	result := make([]NotificationSource, 0, limit)
	err := store.db.View(func(tx *bolt.Tx) error {
		for _, bucketName := range [][]byte{notificationSourceBucket, cellularNotificationSourceBucket} {
			if err := tx.Bucket(bucketName).ForEach(func(_, wire []byte) error {
				var source NotificationSource
				if json.Unmarshal(wire, &source) != nil || source.validate() != nil {
					return errors.New("stored call notification source is invalid")
				}
				if source.Acked || source.NotBefore.After(now) || len(result) >= limit {
					return nil
				}
				result = append(result, source)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].NotBefore.Equal(result[right].NotBefore) {
			return result[left].SourceID < result[right].SourceID
		}
		return result[left].NotBefore.Before(result[right].NotBefore)
	})
	return result, err
}

func (store *Store) AckNotificationSource(sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if (!strings.HasPrefix(sourceID, "vowifi-call-") && !strings.HasPrefix(sourceID, "cellular-call-")) || len(sourceID) > 256 {
		return errors.New("invalid call notification source identity")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(notificationSourceBucket)
		if strings.HasPrefix(sourceID, "cellular-call-") {
			bucket = tx.Bucket(cellularNotificationSourceBucket)
		}
		wire := bucket.Get([]byte(sourceID))
		if wire == nil {
			return nil
		}
		var source NotificationSource
		if json.Unmarshal(wire, &source) != nil || source.validate() != nil {
			return errors.New("stored call notification source is invalid")
		}
		if source.Acked {
			return nil
		}
		source.Acked, source.Peer, source.CardID = true, "", ""
		encoded, err := json.Marshal(source)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(sourceID), encoded)
	})
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
	return store.ListTransport(lineID, "", limit)
}

func (store *Store) ListTransport(lineID, transport string, limit int) ([]Record, error) {
	if transport != "" && transport != "vowifi" && transport != "cellular" {
		return nil, errors.New("invalid call transport")
	}
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
			if (lineID == "" || record.LineID == lineID) && (transport == "" || record.Transport == transport) {
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

// ActiveLine reports whether durable call history still contains an unended
// call for the line. It is a conservative lifecycle preflight only; the call
// owner remains responsible for the actual hangup/cleanup protocol.
func (store *Store) ActiveLine(lineID string) (bool, error) {
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return false, errors.New("line ID is required")
	}
	active := false
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(recordsBucket).ForEach(func(_, value []byte) error {
			record, found, err := decodeRecord(value)
			if err != nil || !found {
				return err
			}
			if record.LineID == lineID && record.EndedAt == nil {
				active = true
			}
			return nil
		})
	})
	return active, err
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

// PurgeLine erases ended call history and notification payload for one line.
// Active calls are rejected rather than converted into terminal evidence.
func (store *Store) PurgeLine(lineID string) error {
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return errors.New("line ID is required")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(purgedLinesBucket).Put([]byte(lineID), []byte{1}); err != nil {
			return err
		}
		for _, bucketName := range [][]byte{recordsBucket, notificationSourceBucket, cellularNotificationSourceBucket, cellularSourceBucket} {
			bucket := tx.Bucket(bucketName)
			var keys [][]byte
			if err := bucket.ForEach(func(key, value []byte) error {
				matches := false
				switch string(bucketName) {
				case string(recordsBucket):
					record, found, err := decodeRecord(value)
					if err != nil || !found {
						return err
					}
					if record.LineID == lineID && record.EndedAt == nil {
						return errors.New("active call history cannot be purged")
					}
					matches = record.LineID == lineID
				case string(cellularSourceBucket):
					var source CellularCallSource
					if json.Unmarshal(value, &source) != nil || source.validate() != nil {
						return errors.New("stored cellular call source is invalid")
					}
					if source.LineID == lineID && source.State != "idle" && source.State != "unavailable" {
						return errors.New("active cellular call source cannot be purged")
					}
					matches = source.LineID == lineID
				default:
					var source NotificationSource
					if json.Unmarshal(value, &source) != nil || source.validate() != nil {
						return errors.New("stored call notification source is invalid")
					}
					matches = source.LineID == lineID
				}
				if matches {
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
		}
		return nil
	})
}

// RetainLine freezes ended call records while erasing current call and
// notification sources. Active calls remain a hard conflict.
func (store *Store) RetainLine(lineID string) error {
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return errors.New("line ID is required")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(recordsBucket).ForEach(func(_, value []byte) error {
			record, found, err := decodeRecord(value)
			if err != nil || !found {
				return err
			}
			if record.LineID == lineID && record.EndedAt == nil {
				return errors.New("active call history cannot be retained")
			}
			return nil
		}); err != nil {
			return err
		}
		if err := tx.Bucket(purgedLinesBucket).Put([]byte(lineID), []byte{1}); err != nil {
			return err
		}
		for _, bucketName := range [][]byte{notificationSourceBucket, cellularNotificationSourceBucket, cellularSourceBucket} {
			bucket := tx.Bucket(bucketName)
			var keys [][]byte
			if err := bucket.ForEach(func(key, value []byte) error {
				matches := false
				if string(bucketName) == string(cellularSourceBucket) {
					var source CellularCallSource
					if json.Unmarshal(value, &source) != nil || source.validate() != nil {
						return errors.New("stored cellular call source is invalid")
					}
					if source.LineID == lineID && source.State != "idle" && source.State != "unavailable" {
						return errors.New("active cellular call source cannot be retained")
					}
					matches = source.LineID == lineID
				} else {
					var source NotificationSource
					if json.Unmarshal(value, &source) != nil || source.validate() != nil {
						return errors.New("stored call notification source is invalid")
					}
					matches = source.LineID == lineID
				}
				if matches {
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
		}
		return nil
	})
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

func validCardID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func terminalStatus(status string) bool {
	return status == "ended" || status == "failed" || status == "missed" || status == "rejected" || status == "interrupted"
}

func oneOfCallState(value string) bool {
	return value == "ringing_in" || value == "active" || value == "held" || value == "idle" || value == "unavailable"
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
