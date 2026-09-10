package events

import (
	"bytes"
	"encoding/json"
	"errors"
	bolt "go.etcd.io/bbolt"
	"slices"
	"sort"
	"time"
)

type EUICCDeletion struct {
	EID             string    `json:"eid"`
	ICCID           string    `json:"iccid"`
	OperationID     string    `json:"operation_id"`
	State           string    `json:"state"`
	Code            string    `json:"code,omitempty"`
	BeforeSequences []int64   `json:"before_sequences"`
	Notifications   []int64   `json:"notifications"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func deletionOperationKey(eid, id string) []byte {
	return []byte("euicc-delete-operation-v1\x00" + eid + "\x00" + id)
}
func (store *BoltStore) EUICCDeletion(eid, id string) (EUICCDeletion, bool, error) {
	var result EUICCDeletion
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketMetadata).Get(deletionOperationKey(eid, id))
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &result)
	})
	return result, found, err
}
func (store *BoltStore) BeginEUICCDeletion(record EUICCDeletion) (EUICCDeletion, bool, error) {
	if _, err := euiccDeletionKey(record.EID, record.ICCID); err != nil {
		return record, false, err
	}
	if !validRecoveryLine(record.OperationID) || len(record.BeforeSequences) > 128 {
		return record, false, errors.New("invalid deletion operation")
	}
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMetadata)
		key := deletionOperationKey(record.EID, record.OperationID)
		if raw := b.Get(key); raw != nil {
			var old EUICCDeletion
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
			if old.ICCID != record.ICCID {
				return errors.New("deletion identity changed")
			}
			record = old
			return nil
		}
		active := []byte("euicc-delete-active-v1\x00" + record.EID + "\x00" + record.ICCID)
		if priorID := b.Get(active); priorID != nil {
			var prior EUICCDeletion
			if err := json.Unmarshal(b.Get(deletionOperationKey(record.EID, string(priorID))), &prior); err != nil {
				return err
			}
			if prior.State == "pending" || prior.State == "unknown" {
				return errors.New("previous deletion requires reconciliation")
			}
		}
		record.State = "pending"
		record.CreatedAt = time.Now().UTC()
		record.UpdatedAt = record.CreatedAt
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err = b.Put(key, raw); err != nil {
			return err
		}
		if err = b.Put(active, []byte(record.OperationID)); err != nil {
			return err
		}
		created = true
		return nil
	})
	return record, created, err
}
func (store *BoltStore) FinishEUICCDeletion(eid, id, state, code string) (EUICCDeletion, error) {
	var record EUICCDeletion
	if state != "deleted" && state != "unknown" && state != "deleted_observed" && state != "not_deleted_observed" {
		return record, errors.New("invalid deletion state")
	}
	if len(code) > 128 {
		return record, errors.New("invalid deletion code")
	}
	err := store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMetadata)
		key := deletionOperationKey(eid, id)
		raw := b.Get(key)
		if raw == nil {
			return errors.New("deletion not found")
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if (record.State == "deleted" || record.State == "deleted_observed") && (state == "unknown" || state == "not_deleted_observed") {
			return nil
		}
		record.State = state
		record.Code = code
		record.UpdatedAt = time.Now().UTC()
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return b.Put(key, raw)
	})
	return record, err
}
func (store *BoltStore) EUICCDeletions(eid string) ([]EUICCDeletion, error) {
	var records []EUICCDeletion
	err := store.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketMetadata).Cursor()
		prefix := []byte("euicc-delete-operation-v1\x00" + eid + "\x00")
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var r EUICCDeletion
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			records = append(records, r)
		}
		return nil
	})
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.Before(records[j].CreatedAt) })
	return records, err
}

func (store *BoltStore) AttachEUICCDeletionNotifications(eid, id string, sequences []int64) error {
	sequences = append([]int64(nil), sequences...)
	slices.Sort(sequences)
	if len(sequences) == 0 || len(sequences) > 128 {
		return errors.New("deletion notifications unavailable")
	}
	for _, s := range sequences {
		if s < 0 {
			return errors.New("invalid notification sequence")
		}
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMetadata)
		key := deletionOperationKey(eid, id)
		var r EUICCDeletion
		if err := json.Unmarshal(b.Get(key), &r); err != nil {
			return err
		}
		if len(r.Notifications) > 0 {
			if slices.Equal(r.Notifications, sequences) {
				return nil
			}
			return errors.New("deletion notification references already fixed")
		}
		r.Notifications = append([]int64(nil), sequences...)
		r.UpdatedAt = time.Now().UTC()
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return b.Put(key, raw)
	})
}
