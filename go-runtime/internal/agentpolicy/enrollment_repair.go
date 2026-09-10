package agentpolicy

import (
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

// RepairDefaultEnrollment is an explicit offline operator recovery, not an
// inventory migration: old baseline records do not encode why they were protected.
func (store *Store) RepairDefaultEnrollment(equipment, card string, firstSeen time.Time) error {
	if !validPair(equipment, card) || firstSeen.IsZero() {
		return errors.New("exact enrollment identity is required")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(discoveryBucket)
		if bucket == nil {
			return errors.New("enrollment not found")
		}
		var record Discovery
		if json.Unmarshal(bucket.Get([]byte(equipment)), &record) != nil || record.EquipmentID != equipment ||
			record.InitializedCard != card || !record.FirstSeen.Equal(firstSeen) || record.ExplicitlyProtected {
			return errors.New("enrollment identity changed or explicitly protected")
		}
		found := false
		if err := tx.Bucket(bucketPolicies).ForEach(func(_, payload []byte) error {
			var policy Policy
			if json.Unmarshal(payload, &policy) != nil || policy.normalizeAndValidate() != nil {
				return errors.New("invalid stored policy")
			}
			if policy.EquipmentID != equipment {
				return nil
			}
			if found || !initializedPolicy(record, policy) {
				return errors.New("equipment policy changed after initialization")
			}
			found = true
			return nil
		}); err != nil {
			return err
		}
		if !found {
			return errors.New("initial policy not found")
		}
		record.Baseline = false
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(equipment), payload)
	})
}
