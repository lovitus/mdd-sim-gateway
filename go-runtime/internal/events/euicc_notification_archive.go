package events

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	bolt "go.etcd.io/bbolt"
	"strconv"
	"time"
)

type EUICCReplayAttempt struct {
	ID    string    `json:"id"`
	State string    `json:"state"`
	At    time.Time `json:"at"`
}
type EUICCNotificationArchive struct {
	EID       string                           `json:"eid"`
	Entry     agentlink.EUICCNotificationEntry `json:"entry"`
	Payload   []byte                           `json:"payload"`
	SHA256    string                           `json:"sha256"`
	CreatedAt time.Time                        `json:"created_at"`
	Attempts  []EUICCReplayAttempt             `json:"attempts"`
}

func notificationArchiveKey(eid string, seq int64) []byte {
	return []byte("euicc-notification-v1\x00" + eid + "\x00" + strconv.FormatInt(seq, 10))
}
func (store *BoltStore) SaveEUICCNotification(eid string, entry agentlink.EUICCNotificationEntry, payload []byte) (EUICCNotificationArchive, error) {
	if _, err := euiccDeletionKey(eid, entry.ICCID); err != nil {
		return EUICCNotificationArchive{}, err
	}
	if entry.Validate() != nil || entry.Event != "delete" || len(payload) == 0 || len(payload) > 65536 {
		return EUICCNotificationArchive{}, errors.New("invalid signed notification archive")
	}
	digest := sha256.Sum256(payload)
	archive := EUICCNotificationArchive{EID: eid, Entry: entry, Payload: append([]byte(nil), payload...), SHA256: hex.EncodeToString(digest[:]), CreatedAt: time.Now().UTC()}
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketMetadata)
		key := notificationArchiveKey(eid, entry.SequenceNumber)
		if raw := bucket.Get(key); raw != nil {
			var prior EUICCNotificationArchive
			if err := json.Unmarshal(raw, &prior); err != nil {
				return err
			}
			if prior.Entry != entry || prior.SHA256 != archive.SHA256 {
				return errors.New("notification archive identity changed")
			}
			archive = prior
			return nil
		}
		raw, err := json.Marshal(archive)
		if err != nil {
			return err
		}
		return bucket.Put(key, raw)
	})
	return archive, err
}
func (store *BoltStore) EUICCNotificationArchive(eid string, seq int64) (EUICCNotificationArchive, error) {
	var archive EUICCNotificationArchive
	err := store.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketMetadata).Get(notificationArchiveKey(eid, seq))
		if raw == nil {
			return errors.New("signed notification not archived")
		}
		return json.Unmarshal(raw, &archive)
	})
	return archive, err
}
func (store *BoltStore) EUICCNotificationArchives(eid string) ([]EUICCNotificationArchive, error) {
	var all []EUICCNotificationArchive
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketMetadata).Cursor()
		prefix := []byte("euicc-notification-v1\x00" + eid + "\x00")
		for k, v := cursor.Seek(prefix); k != nil && len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix); k, v = cursor.Next() {
			var a EUICCNotificationArchive
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			a.Payload = nil
			all = append(all, a)
		}
		return nil
	})
	return all, err
}
func (store *BoltStore) BeginEUICCReplay(eid string, seq int64, id, hash string) (EUICCNotificationArchive, bool, error) {
	var a EUICCNotificationArchive
	created := false
	if !validRecoveryLine(id) {
		return a, false, errors.New("invalid replay identity")
	}
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketMetadata)
		key := notificationArchiveKey(eid, seq)
		raw := bucket.Get(key)
		if raw == nil {
			return errors.New("signed notification not archived")
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if a.SHA256 != hash {
			return errors.New("notification archive changed")
		}
		for _, prior := range a.Attempts {
			if prior.ID == id {
				return nil
			}
		}
		if len(a.Attempts) >= 1024 {
			return errors.New("replay history capacity reached")
		}
		a.Attempts = append(a.Attempts, EUICCReplayAttempt{ID: id, State: "unknown", At: time.Now().UTC()})
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		if err = bucket.Put(key, raw); err != nil {
			return err
		}
		created = true
		return nil
	})
	return a, created, err
}
func (store *BoltStore) FinishEUICCReplay(eid string, seq int64, id, state string) error {
	if state != "acknowledged" && state != "failed" && state != "unknown" {
		return errors.New("invalid replay result")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMetadata)
		k := notificationArchiveKey(eid, seq)
		var a EUICCNotificationArchive
		if err := json.Unmarshal(b.Get(k), &a); err != nil {
			return err
		}
		found := false
		for i := range a.Attempts {
			if a.Attempts[i].ID == id {
				if a.Attempts[i].State != "unknown" {
					return nil
				}
				a.Attempts[i].State = state
				found = true
				break
			}
		}
		if !found {
			return errors.New("replay attempt missing")
		}
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		return b.Put(k, raw)
	})
}
