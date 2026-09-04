// Package agentpolicy owns durable, exact modem+SIM policy on an Agent host.
// Ephemeral attachment and process generations are deliberately excluded from
// the store; they remain per-operation transport fences.
package agentpolicy

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

const SchemaVersion = 1

var (
	bucketMeta     = []byte("meta")
	bucketPolicies = []byte("policies-v1")
	bucketProfiles = []byte("profiles-v1")
	keySchema      = []byte("schema")
)

type Desired struct {
	CellularEnabled   bool   `json:"cellular_enabled"`
	ConnectionEnabled bool   `json:"connection_enabled"`
	FlightMode        bool   `json:"flight_mode"`
	RoamingEnabled    bool   `json:"roaming_enabled"`
	SelectedProfile   string `json:"selected_profile,omitempty"`
}

type Policy struct {
	SchemaVersion int       `json:"schema_version"`
	EquipmentID   string    `json:"equipment_id"`
	CardID        string    `json:"card_id"`
	Revision      uint64    `json:"revision"`
	Desired       Desired   `json:"desired"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Profile struct {
	Name     string `json:"name"`
	APN      string `json:"apn"`
	Auth     string `json:"auth"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type Store struct{ db *bolt.DB }

func Open(path string, timeout time.Duration) (*Store, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) || timeout <= 0 {
		return nil, errors.New("invalid modem policy store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(bucketMeta)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketPolicies); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketProfiles); err != nil {
			return err
		}
		stored := meta.Get(keySchema)
		if stored == nil {
			return meta.Put(keySchema, uint64Bytes(SchemaVersion))
		}
		if bytesUint64(stored) != SchemaVersion {
			return errors.New("unsupported modem policy store schema")
		}
		return nil
	}); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func Default(equipmentID, cardID string) Policy {
	return Policy{SchemaVersion: SchemaVersion, EquipmentID: equipmentID, CardID: cardID,
		Desired: Desired{CellularEnabled: false, ConnectionEnabled: false, FlightMode: false, RoamingEnabled: false}}
}

func (store *Store) Get(equipmentID, cardID string) (Policy, bool, error) {
	if !validPair(equipmentID, cardID) {
		return Policy{}, false, errors.New("invalid modem policy identity")
	}
	result := Default(equipmentID, cardID)
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		payload := tx.Bucket(bucketPolicies).Get(pairKey(equipmentID, cardID))
		if payload == nil {
			return nil
		}
		found = true
		if json.Unmarshal(payload, &result) != nil || result.normalizeAndValidate() != nil ||
			result.EquipmentID != equipmentID || result.CardID != cardID || result.Revision == 0 {
			return errors.New("stored modem policy is invalid")
		}
		return nil
	})
	return result, found, err
}

func (store *Store) PutExpected(input Policy, expected uint64) (Policy, error) {
	policy := input
	if policy.SchemaVersion == 0 {
		policy.SchemaVersion = SchemaVersion
	}
	if err := policy.normalizeAndValidate(); err != nil {
		return Policy{}, err
	}
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketPolicies)
		key := pairKey(policy.EquipmentID, policy.CardID)
		currentRevision := uint64(0)
		if payload := bucket.Get(key); payload != nil {
			var current Policy
			if json.Unmarshal(payload, &current) != nil || current.normalizeAndValidate() != nil {
				return errors.New("stored modem policy is invalid")
			}
			currentRevision = current.Revision
		}
		if currentRevision != expected {
			return ErrRevision
		}
		policy.Revision = currentRevision + 1
		policy.UpdatedAt = time.Now().UTC()
		payload, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		return bucket.Put(key, payload)
	})
	return policy, err
}

func (store *Store) Profiles(equipmentID, cardID string) ([]Profile, error) {
	if !validPair(equipmentID, cardID) {
		return nil, errors.New("invalid modem profile identity")
	}
	result := []Profile{}
	err := store.db.View(func(tx *bolt.Tx) error {
		prefix := append(pairKey(equipmentID, cardID), 0)
		cursor := tx.Bucket(bucketProfiles).Cursor()
		for key, payload := cursor.Seek(prefix); key != nil && strings.HasPrefix(string(key), string(prefix)); key, payload = cursor.Next() {
			var profile Profile
			if json.Unmarshal(payload, &profile) != nil || profile.normalizeAndValidate() != nil {
				return errors.New("stored modem profile is invalid")
			}
			result = append(result, profile)
		}
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, err
}

func (store *Store) Profile(equipmentID, cardID, name string) (Profile, bool, error) {
	name = strings.TrimSpace(name)
	if !validPair(equipmentID, cardID) || !validText(name, 100) {
		return Profile{}, false, errors.New("invalid modem profile identity")
	}
	var result Profile
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		payload := tx.Bucket(bucketProfiles).Get(profileKey(equipmentID, cardID, name))
		if payload == nil {
			return nil
		}
		found = true
		if json.Unmarshal(payload, &result) != nil || result.normalizeAndValidate() != nil {
			return errors.New("stored modem profile is invalid")
		}
		return nil
	})
	return result, found, err
}

// SaveProfileExpected atomically persists the desired platform profile and
// selects it before the platform mutation. Empty password with keepPassword
// retains an existing secret; secrets never leave this owner-only database
// through a view method. A failed platform mutation therefore remains a
// visible desired/actual mismatch instead of losing the user's intent.
func (store *Store) SaveProfileExpected(equipmentID, cardID string, input Profile,
	keepPassword bool, expected uint64) (Policy, error) {
	profile := input
	if !validPair(equipmentID, cardID) || profile.normalizeAndValidate() != nil {
		return Policy{}, errors.New("invalid modem profile")
	}
	policy := Default(equipmentID, cardID)
	err := store.db.Update(func(tx *bolt.Tx) error {
		policies, profiles := tx.Bucket(bucketPolicies), tx.Bucket(bucketProfiles)
		key := pairKey(equipmentID, cardID)
		if payload := policies.Get(key); payload != nil {
			if json.Unmarshal(payload, &policy) != nil || policy.normalizeAndValidate() != nil {
				return errors.New("stored modem policy is invalid")
			}
		}
		if policy.Revision != expected {
			return ErrRevision
		}
		storedProfileKey := profileKey(equipmentID, cardID, profile.Name)
		if keepPassword {
			if payload := profiles.Get(storedProfileKey); payload != nil {
				var previous Profile
				if json.Unmarshal(payload, &previous) != nil || previous.normalizeAndValidate() != nil {
					return errors.New("stored modem profile is invalid")
				}
				profile.Password = previous.Password
			}
		}
		profilePayload, err := json.Marshal(profile)
		if err != nil {
			return err
		}
		if err := profiles.Put(storedProfileKey, profilePayload); err != nil {
			return err
		}
		policy.SchemaVersion, policy.EquipmentID, policy.CardID = SchemaVersion, equipmentID, cardID
		policy.Desired.SelectedProfile = profile.Name
		policy.Revision++
		policy.UpdatedAt = time.Now().UTC()
		policyPayload, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		return policies.Put(key, policyPayload)
	})
	return policy, err
}

func (policy *Policy) normalizeAndValidate() error {
	if policy.SchemaVersion != SchemaVersion || !validPair(policy.EquipmentID, policy.CardID) ||
		len(policy.Desired.SelectedProfile) > 100 || containsControl(policy.Desired.SelectedProfile) {
		return errors.New("invalid modem policy")
	}
	policy.Desired.SelectedProfile = strings.TrimSpace(policy.Desired.SelectedProfile)
	return nil
}

func (profile *Profile) normalizeAndValidate() error {
	profile.Name, profile.APN = strings.TrimSpace(profile.Name), strings.TrimSpace(profile.APN)
	profile.Auth = strings.ToUpper(strings.TrimSpace(profile.Auth))
	profile.Username = strings.TrimSpace(profile.Username)
	if profile.Auth == "" {
		profile.Auth = "NONE"
	}
	if !validText(profile.Name, 100) || !validText(profile.APN, 100) ||
		!oneOf(profile.Auth, "NONE", "PAP", "CHAP", "MSCHAPV2") ||
		len(profile.Username) > 200 || len(profile.Password) > 500 ||
		containsControl(profile.Username) || containsControl(profile.Password) {
		return errors.New("invalid modem profile")
	}
	return nil
}

var ErrRevision = errors.New("modem policy revision changed")

func pairKey(equipmentID, cardID string) []byte { return []byte(equipmentID + "\x00" + cardID) }
func profileKey(equipmentID, cardID, name string) []byte {
	return []byte(equipmentID + "\x00" + cardID + "\x00" + name)
}
func validPair(equipmentID, cardID string) bool {
	return digits(equipmentID, 14, 17) && digits(cardID, 4, 32)
}
func digits(value string, min, max int) bool {
	if len(value) < min || len(value) > max {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
func validText(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= max && !containsControl(value)
}
func containsControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}
func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
func uint64Bytes(value uint64) []byte {
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, value)
	return result
}
func bytesUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
