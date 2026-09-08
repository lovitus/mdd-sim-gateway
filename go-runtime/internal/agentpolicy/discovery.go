package agentpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"

	bolt "go.etcd.io/bbolt"
)

var discoveryBucket = []byte("equipment-discovery-v1")
var discoveryBaselineKey = []byte("equipment-discovery-baseline-v1")

// InitialDefaults is a received Core intent, not inferred from a timestamp.
// Borrowing permission and per-SIM APN selection are never implicit defaults.
type InitialDefaults = agentlink.DeviceDefaults

// BindInitialDefaults receives an authenticated session's current template.
// nil disables capture of new intent (for example after that session closes).
// It changes no existing discovery record or per-device policy.
func (manager *Manager) BindInitialDefaults(defaults *InitialDefaults) error {
	var copy *InitialDefaults
	if defaults != nil {
		if err := defaults.Validate(); err != nil {
			return err
		}
		value := *defaults
		copy = &value
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if copy != nil && manager.initialDefaults != nil && copy.Authority == manager.initialDefaults.Authority {
		if copy.Revision < manager.initialDefaults.Revision || (copy.Revision == manager.initialDefaults.Revision && *copy != *manager.initialDefaults) {
			return errors.New("initial device default revision is stale or conflicting")
		}
	}
	manager.initialDefaults = copy
	manager.defaultsReceived = true
	return nil
}

func (manager *Manager) ClearInitialDefaults() {
	manager.mu.Lock()
	manager.initialDefaults = nil
	manager.defaultsReceived = false
	manager.mu.Unlock()
}

func (store *Store) discoveryBaselineEstablished() (bool, error) {
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		marker := tx.Bucket(bucketMeta).Get(discoveryBaselineKey)
		if marker == nil {
			return nil
		}
		if !bytes.Equal(marker, []byte{1}) {
			return errors.New("stored discovery baseline is invalid")
		}
		found = true
		return nil
	})
	return found, err
}

func (manager *Manager) observeDiscovery(facts []agentmodem.Fact) {
	var equipment, protected []string
	var err error
	if manager.config.ProtectedEquipment != nil {
		protected, err = manager.config.ProtectedEquipment()
	}
	for _, fact := range facts {
		if fact.EquipmentID != "" {
			equipment = append(equipment, fact.EquipmentID)
		}
	}
	var records []Discovery
	manager.mu.RLock()
	received := manager.defaultsReceived
	var defaults *InitialDefaults
	if manager.initialDefaults != nil {
		value := *manager.initialDefaults
		defaults = &value
	}
	manager.mu.RUnlock()
	if err == nil && manager.config.InitialInventoryDefaults && !received {
		ready, readErr := manager.config.Store.discoveryBaselineEstablished()
		if readErr != nil {
			err = readErr
		} else if !ready {
			err = errors.New("initial Agent enrollment awaits Core defaults")
		}
	}
	if err == nil {
		records, err = manager.config.Store.observeEquipmentForEnrollment(equipment, protected, manager.config.Now(), defaults, manager.config.InitialInventoryDefaults && received)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.discoveryErr = err
	manager.discovery = nil
	if err == nil {
		manager.discovery = make(map[string]Discovery, len(records))
		for _, record := range records {
			manager.discovery[record.EquipmentID] = record
		}
	}
}

// DiscoveryFor reports the last successful inventory observation, not a new
// hardware scan. Absent or failed observation cannot authorize defaulting.
func (manager *Manager) DiscoveryFor(equipmentID string) (Discovery, bool, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.discoveryErr != nil {
		return Discovery{}, false, manager.discoveryErr
	}
	record, found := manager.discovery[equipmentID]
	if record.Defaults != nil {
		value := *record.Defaults
		record.Defaults = &value
	}
	return record, found, nil
}

// Discovery is equipment-scoped: replacing a SIM is not discovering hardware.
// The first complete inventory establishes a baseline without adopting policy.
// This record is evidence for enrollment, not permission to touch the device.
type Discovery struct {
	EquipmentID     string           `json:"equipment_id"`
	FirstSeen       time.Time        `json:"first_seen"`
	Baseline        bool             `json:"baseline"`
	Defaults        *InitialDefaults `json:"defaults,omitempty"`
	InitializedCard string           `json:"initialized_card,omitempty"`
}

func (store *Store) initializeDiscoveredPolicy(equipmentID, cardID string, expected InitialDefaults) (Policy, bool, error) {
	if !validPair(equipmentID, cardID) || expected.Validate() != nil {
		return Policy{}, false, errors.New("invalid default initialization identity")
	}
	var result Policy
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		discoveries := tx.Bucket(discoveryBucket)
		if discoveries == nil {
			return nil
		}
		var record Discovery
		payload := discoveries.Get([]byte(equipmentID))
		if payload == nil {
			return nil
		}
		if json.Unmarshal(payload, &record) != nil || record.EquipmentID != equipmentID || record.FirstSeen.IsZero() {
			return errors.New("invalid stored discovery")
		}
		if record.Baseline || record.InitializedCard != "" || record.Defaults == nil || *record.Defaults != expected {
			return nil
		}
		policies := tx.Bucket(bucketPolicies)
		// Any existing SIM policy on this equipment represents prior ownership.
		if err := policies.ForEach(func(_, payload []byte) error {
			var policy Policy
			if json.Unmarshal(payload, &policy) != nil || policy.normalizeAndValidate() != nil {
				return errors.New("stored modem policy is invalid")
			}
			if policy.EquipmentID == equipmentID {
				return ErrRevision
			}
			return nil
		}); err != nil {
			return err
		}
		result = Default(equipmentID, cardID)
		result.Desired.ConnectionEnabled = expected.ConnectionEnabled
		result.Desired.FlightMode = expected.FlightMode
		result.Desired.RoamingEnabled = expected.RoamingEnabled
		result.Revision = 1
		result.UpdatedAt = time.Now().UTC()
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := policies.Put(pairKey(equipmentID, cardID), payload); err != nil {
			return err
		}
		record.InitializedCard = cardID
		payload, err = json.Marshal(record)
		if err != nil {
			return err
		}
		if err := discoveries.Put([]byte(equipmentID), payload); err != nil {
			return err
		}
		created = true
		return nil
	})
	return result, created, err
}

func (manager *Manager) initializeDiscoveryPolicy(ctx context.Context, fact agentmodem.Fact) (Policy, bool, error) {
	record, found, err := manager.DiscoveryFor(fact.EquipmentID)
	if err != nil || !found || record.Baseline || record.Defaults == nil || record.InitializedCard != "" {
		return Policy{}, false, err
	}
	var policy Policy
	created := false
	err = manager.coordinatorNow().DoAuxiliary(ctx, fact.EquipmentID, func(operationContext context.Context) error {
		if manager.config.ProtectedEquipment != nil {
			protected, err := manager.config.ProtectedEquipment()
			if err != nil {
				return err
			}
			for _, id := range protected {
				if id == fact.EquipmentID {
					return nil
				}
			}
		}
		target := Target{EquipmentID: fact.EquipmentID, CardID: fact.SIM.ICCID, AttachmentID: fact.AttachmentID, SIMSessionGeneration: fact.SIM.SessionGeneration}
		if err := manager.requireFresh(operationContext, target); err != nil {
			return err
		}
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		if manager.initialDefaults == nil || manager.initialDefaults.Authority != record.Defaults.Authority || manager.initialDefaults.Revision < record.Defaults.Revision {
			return nil
		}
		var err error
		policy, created, err = manager.config.Store.initializeDiscoveredPolicy(fact.EquipmentID, fact.SIM.ICCID, *record.Defaults)
		return err
	})
	return policy, created, err
}

// ObserveEquipment requires a complete successful inventory. protected also
// includes equipment with local mode/capture choices, even when disconnected.
// Existing policy records are protected automatically in this transaction.
// A failed/partial probe must not call this method with an empty replacement.
func (store *Store) ObserveEquipment(equipment, protected []string, observedAt time.Time) ([]Discovery, error) {
	return store.observeEquipment(equipment, protected, observedAt, nil)
}

func (store *Store) observeEquipment(equipment, protected []string, observedAt time.Time, defaults *InitialDefaults) ([]Discovery, error) {
	return store.observeEquipmentForEnrollment(equipment, protected, observedAt, defaults, false)
}

func (store *Store) observeEquipmentForEnrollment(equipment, protected []string, observedAt time.Time, defaults *InitialDefaults, initialDefaults bool) ([]Discovery, error) {
	if defaults != nil {
		if err := defaults.Validate(); err != nil {
			return nil, err
		}
	}
	if observedAt.IsZero() {
		return nil, errors.New("equipment observation time is required")
	}
	validID := func(id string) bool {
		return len(id) >= 14 && len(id) <= 16 && strings.IndexFunc(id, func(r rune) bool { return r < '0' || r > '9' }) < 0
	}
	current, protect := map[string]bool{}, map[string]bool{}
	for _, id := range equipment {
		if !validID(id) {
			return nil, errors.New("invalid discovered equipment identity")
		}
		current[id] = true
	}
	for _, id := range protected {
		if !validID(id) {
			return nil, errors.New("invalid protected equipment identity")
		}
		protect[id] = true
	}
	var result []Discovery
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(discoveryBucket)
		if err != nil {
			return err
		}
		meta := tx.Bucket(bucketMeta)
		baseline := meta.Get(discoveryBaselineKey) == nil
		if marker := meta.Get(discoveryBaselineKey); marker != nil && !bytes.Equal(marker, []byte{1}) {
			return errors.New("stored equipment discovery baseline is invalid")
		}
		if err := tx.Bucket(bucketPolicies).ForEach(func(_, value []byte) error {
			var policy Policy
			if json.Unmarshal(value, &policy) != nil || policy.normalizeAndValidate() != nil || policy.Revision == 0 {
				return errors.New("stored modem policy is invalid")
			}
			protect[policy.EquipmentID] = true
			return nil
		}); err != nil {
			return err
		}
		all := map[string]bool{}
		for id := range current {
			all[id] = true
		}
		for id := range protect {
			all[id] = true
		}
		for id := range all {
			record := Discovery{EquipmentID: id, FirstSeen: observedAt.UTC(), Baseline: (baseline && !initialDefaults) || protect[id]}
			if !record.Baseline && defaults != nil {
				value := *defaults
				record.Defaults = &value
			}
			previous := bucket.Get([]byte(id))
			if previous != nil {
				record = Discovery{}
				if json.Unmarshal(previous, &record) != nil || record.EquipmentID != id || record.FirstSeen.IsZero() {
					return errors.New("stored equipment discovery is invalid")
				}
				if record.Defaults != nil && record.Defaults.Validate() != nil {
					return errors.New("stored initial device defaults are invalid")
				}
				// Explicit user policy always outranks potential enrollment.
				if protect[id] {
					record.Baseline = true
				}
			}
			payload, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if !bytes.Equal(previous, payload) {
				if err := bucket.Put([]byte(id), payload); err != nil {
					return err
				}
			}
			if current[id] {
				result = append(result, record)
			}
		}
		if baseline {
			return meta.Put(discoveryBaselineKey, []byte{1})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EquipmentID < result[j].EquipmentID })
	return result, nil
}
