package linecatalog

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"
)

const RawModemBindingSchemaVersion = 1

// RawModemBinding is the only durable raw-device intent. Every runtime fence
// (Agent process, attachment, SIM insertion, USB session, stream and token) is
// intentionally absent and must be resolved again from fresh Agent facts.
type RawModemBinding struct {
	SchemaVersion   int    `json:"schema_version"`
	Epoch           uint64 `json:"epoch"`
	LineID          string `json:"line_id"`
	SourceAgentID   string `json:"source_agent_id"`
	EquipmentID     string `json:"equipment_id"`
	CardID          string `json:"card_id"`
	ImporterAgentID string `json:"importer_agent_id"`
	Enabled         bool   `json:"enabled"`
}

type RawModemSnapshot struct {
	SchemaVersion int               `json:"schema_version"`
	Revision      uint64            `json:"revision"`
	Bindings      []RawModemBinding `json:"bindings"`
}

func (binding *RawModemBinding) normalizeAndValidate() error {
	if binding == nil {
		return errors.New("raw modem binding is nil")
	}
	binding.LineID = strings.TrimSpace(binding.LineID)
	binding.SourceAgentID = strings.TrimSpace(binding.SourceAgentID)
	binding.EquipmentID = digitsOnly(binding.EquipmentID)
	binding.CardID = digitsOnly(binding.CardID)
	binding.ImporterAgentID = strings.TrimSpace(binding.ImporterAgentID)
	if binding.SchemaVersion == 0 {
		binding.SchemaVersion = RawModemBindingSchemaVersion
	}
	if binding.SchemaVersion != RawModemBindingSchemaVersion || !validIdentifier(binding.LineID) ||
		!validIdentifier(binding.SourceAgentID) || !digitsBetween(binding.EquipmentID, 14, 16) ||
		!digitsBetween(binding.CardID, 4, 32) || !validIdentifier(binding.ImporterAgentID) ||
		binding.SourceAgentID == binding.ImporterAgentID {
		return errors.New("raw modem binding identity is invalid")
	}
	return nil
}

func (store *Store) RawModemBindings() (RawModemSnapshot, error) {
	result := RawModemSnapshot{SchemaVersion: RawModemBindingSchemaVersion, Bindings: []RawModemBinding{}}
	err := store.db.View(func(transaction *bolt.Tx) error {
		result.Revision = bytesUint64(transaction.Bucket(metadataBucket).Get(rawModemRevisionKey))
		return transaction.Bucket(rawModemBindingsBucket).ForEach(func(_, payload []byte) error {
			var binding RawModemBinding
			if err := json.Unmarshal(payload, &binding); err != nil {
				return errors.New("stored raw modem binding is corrupt")
			}
			if err := binding.normalizeAndValidate(); err != nil || binding.Epoch == 0 {
				return errors.New("stored raw modem binding is invalid")
			}
			result.Bindings = append(result.Bindings, binding)
			return nil
		})
	})
	sort.Slice(result.Bindings, func(left, right int) bool { return result.Bindings[left].LineID < result.Bindings[right].LineID })
	return result, err
}

// PutRawModemBindingExpected validates the durable triple against the current
// line in the same Bolt transaction and advances only the independent raw
// binding revision. Provider/catalog revisions are untouched.
func (store *Store) PutRawModemBindingExpected(input RawModemBinding,
	expectedRevision uint64) (RawModemBinding, uint64, bool, error) {
	binding := input
	binding.Epoch = 0
	if err := binding.normalizeAndValidate(); err != nil {
		return RawModemBinding{}, 0, false, err
	}
	var revision uint64
	changed := false
	err := store.db.Update(func(transaction *bolt.Tx) error {
		metadata := transaction.Bucket(metadataBucket)
		revision = bytesUint64(metadata.Get(rawModemRevisionKey))
		if revision != expectedRevision {
			return ErrRawModemRevision
		}
		bucket := transaction.Bucket(rawModemBindingsBucket)
		var stored RawModemBinding
		hasStored := false
		if current := bucket.Get([]byte(binding.LineID)); current != nil {
			if json.Unmarshal(current, &stored) != nil || stored.normalizeAndValidate() != nil || stored.Epoch == 0 {
				return errors.New("stored raw modem binding is corrupt")
			}
			hasStored = true
			if sameRawModemBinding(stored, binding) {
				binding = stored
				return nil
			}
		}
		if !binding.Enabled {
			if !hasStored || !sameRawModemIdentity(stored, binding) {
				return ErrRawModemRevision
			}
		} else {
			linePayload := transaction.Bucket(linesBucket).Get([]byte(binding.LineID))
			if linePayload == nil {
				return ErrNotFound
			}
			var line Line
			if json.Unmarshal(linePayload, &line) != nil {
				return errors.New("stored line is corrupt")
			}
			if line.CardID != binding.CardID {
				return errors.New("raw modem binding does not match the current line identity")
			}
		}
		if binding.Enabled {
			err := bucket.ForEach(func(key, value []byte) error {
				if string(key) == binding.LineID {
					return nil
				}
				var other RawModemBinding
				if json.Unmarshal(value, &other) != nil || other.normalizeAndValidate() != nil {
					return errors.New("stored raw modem binding is corrupt")
				}
				if other.Enabled && other.SourceAgentID == binding.SourceAgentID &&
					other.EquipmentID == binding.EquipmentID && other.CardID == binding.CardID {
					return ErrRawModemBindingInUse
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		revision++
		binding.Epoch = revision
		payload, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(binding.LineID), payload); err != nil {
			return err
		}
		changed = true
		return metadata.Put(rawModemRevisionKey, uint64Bytes(revision))
	})
	return binding, revision, changed, err
}

func sameRawModemBinding(left, right RawModemBinding) bool {
	return sameRawModemIdentity(left, right) && left.Enabled == right.Enabled
}

func sameRawModemIdentity(left, right RawModemBinding) bool {
	return left.SchemaVersion == right.SchemaVersion && left.LineID == right.LineID &&
		left.SourceAgentID == right.SourceAgentID && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID && left.ImporterAgentID == right.ImporterAgentID
}
