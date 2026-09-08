package events

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var adminAuditBucket = []byte("admin-audit-v1")

const maximumAdminAuditRecords = 10000

// Adapted from ec620942 main.py:_write_audit_record. The existing events
// backup now includes this log; no request bodies or credentials are stored.
type AdminAuditRecord struct {
	At     time.Time `json:"at"`
	Method string    `json:"method"`
	Route  string    `json:"route"`
	Status int       `json:"status"`
	Client string    `json:"client"`
}

func (record AdminAuditRecord) validate() error {
	if record.At.IsZero() || record.Status < 200 || record.Status > 599 || len(record.Route) > 512 || record.Route == "" || strings.ContainsAny(record.Route, "\r\n?") {
		return errors.New("invalid administrative audit record")
	}
	switch record.Method {
	case "POST", "PUT", "PATCH", "DELETE":
	default:
		return errors.New("invalid audit method")
	}
	if record.Client != "" {
		if _, err := netip.ParseAddr(record.Client); err != nil {
			return errors.New("invalid audit client")
		}
	}
	return nil
}

func (store *BoltStore) AppendAdminAudit(record AdminAuditRecord) error {
	if err := record.validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(adminAuditBucket)
		if err != nil {
			return err
		}
		sequence, err := bucket.NextSequence()
		if err != nil {
			return err
		}
		if err := bucket.Put(encodeUint64(sequence), payload); err != nil {
			return err
		}
		if sequence > maximumAdminAuditRecords {
			return bucket.Delete(encodeUint64(sequence - maximumAdminAuditRecords))
		}
		return nil
	})
}

func (store *BoltStore) AdminAudit(limit int) ([]AdminAuditRecord, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid audit limit")
	}
	result := []AdminAuditRecord{}
	err := store.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(adminAuditBucket)
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		for key, payload := cursor.Last(); key != nil && len(result) < limit; key, payload = cursor.Prev() {
			var record AdminAuditRecord
			if err := json.Unmarshal(payload, &record); err != nil || record.validate() != nil {
				return errors.New("stored administrative audit is invalid")
			}
			result = append(result, record)
		}
		return nil
	})
	return result, err
}
