package events

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestAdminAuditIsBoundedAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	store, err := OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	record := AdminAuditRecord{At: time.Now().UTC(), Method: "PATCH", Route: "/v1/system/preferences", Status: 200, Client: "192.0.2.1"}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(adminAuditBucket)
		if err != nil {
			return err
		}
		if err := bucket.Put(encodeUint64(1), payload); err != nil {
			return err
		}
		return bucket.SetSequence(maximumAdminAuditRecords)
	}); err != nil {
		t.Fatal(err)
	}
	record.Status = 412
	if err := store.AppendAdminAudit(record); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entries, err := store.AdminAudit(200)
	if err != nil || len(entries) != 1 || entries[0].Status != 412 {
		t.Fatal(entries, err)
	}
	if _, err := store.AdminAudit(201); err == nil {
		t.Fatal("unbounded audit read accepted")
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(adminAuditBucket).Put(encodeUint64(maximumAdminAuditRecords+2), []byte(`{}`))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdminAudit(200); err == nil {
		t.Fatal("corrupt record became a zero-valued audit event")
	}
}
