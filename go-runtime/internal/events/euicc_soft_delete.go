package events

import (
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A local deletion event is deliberately distinct from a signed card notification.
// Records are keyed by eUICC/profile, never by a deletable MDD line identifier.
type EUICCSoftDeleteEvent struct {
	EID               string    `json:"eid"`
	ICCID             string    `json:"iccid"`
	OperationID       string    `json:"operation_id"`
	OriginalNickname  string    `json:"original_nickname"`
	Marker            string    `json:"marker"`
	State             string    `json:"state"`
	NotificationState string    `json:"notification_state"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func euiccDeletionKey(eid, iccid string) ([]byte, error) {
	if len(eid) != 32 || len(iccid) < 10 || len(iccid) > 22 {
		return nil, errors.New("invalid eUICC deletion identity")
	}
	for _, value := range []string{eid, iccid} {
		for _, c := range value {
			if c < '0' || c > '9' {
				return nil, errors.New("invalid eUICC deletion identity")
			}
		}
	}
	return []byte("euicc-soft-delete-v1\x00" + eid + "\x00" + iccid), nil
}

func (store *BoltStore) EUICCSoftDelete(eid, iccid string) (EUICCSoftDeleteEvent, bool, error) {
	key, err := euiccDeletionKey(eid, iccid)
	if err != nil {
		return EUICCSoftDeleteEvent{}, false, err
	}
	var result EUICCSoftDeleteEvent
	found := false
	err = store.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketMetadata).Get(key)
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &result)
	})
	return result, found, err
}

func (store *BoltStore) BeginEUICCSoftDelete(event EUICCSoftDeleteEvent) (EUICCSoftDeleteEvent, error) {
	key, err := euiccDeletionKey(event.EID, event.ICCID)
	if err != nil {
		return event, err
	}
	if !validRecoveryLine(event.OperationID) || len(event.OriginalNickname) > 256 || event.Marker == "" || len(event.Marker) > 32 {
		return event, errors.New("invalid eUICC deletion event")
	}
	event.State = "pending"
	event.NotificationState = "signed_delete_notification_unavailable"
	event.CreatedAt = time.Now().UTC()
	event.UpdatedAt = event.CreatedAt
	err = store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketMetadata)
		if raw := bucket.Get(key); raw != nil {
			var prior EUICCSoftDeleteEvent
			if err := json.Unmarshal(raw, &prior); err != nil {
				return err
			}
			if prior.OperationID != event.OperationID || prior.OriginalNickname != event.OriginalNickname || prior.Marker != event.Marker {
				return errors.New("eUICC deletion event already exists")
			}
			event = prior
			return nil
		}
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		return bucket.Put(key, raw)
	})
	return event, err
}

func (store *BoltStore) FinishEUICCSoftDelete(eid, iccid, operationID, state string) (EUICCSoftDeleteEvent, error) {
	key, err := euiccDeletionKey(eid, iccid)
	if err != nil {
		return EUICCSoftDeleteEvent{}, err
	}
	if state != "marked" && state != "unknown" {
		return EUICCSoftDeleteEvent{}, errors.New("invalid eUICC deletion outcome")
	}
	var event EUICCSoftDeleteEvent
	err = store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketMetadata)
		raw := bucket.Get(key)
		if raw == nil {
			return errors.New("eUICC deletion event not found")
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return err
		}
		if event.OperationID != operationID {
			return errors.New("eUICC deletion operation changed")
		}
		if event.State == "marked" {
			return nil
		}
		event.State = state
		event.UpdatedAt = time.Now().UTC()
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		return bucket.Put(key, encoded)
	})
	return event, err
}
