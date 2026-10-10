package linecatalog

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"
)

const IMEIPoolSchemaVersion = 1

var (
	ErrIMEIPoolRevision  = errors.New("IMEI pool revision does not match")
	ErrIMEIEntryNotFound = errors.New("IMEI pool entry not found")
	ErrIMEIValueExists   = errors.New("IMEI already belongs to another pool entry")
	ErrIMEIValueInUse    = errors.New("IMEI is bound to one or more lines")
	ErrIMEIBinding       = errors.New("line presentation IMEI does not match the requested pool entry")
	errIMEIFound         = errors.New("IMEI line binding found")
)

// IMEIPoolEntry is an administrator-owned presentation identity. It is not a
// physical modem identity and is never consulted by adapted or raw routing.
type IMEIPoolEntry struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	IMEI          string `json:"imei"`
	Notes         string `json:"notes,omitempty"`
}

type IMEILineBinding struct {
	EntryID  string `json:"entry_id,omitempty"`
	LineID   string `json:"line_id,omitempty"`
	LineName string `json:"line_name,omitempty"`
	CardID   string `json:"card_id"`
	IMEI     string `json:"imei"`
}

type IMEIPoolSnapshot struct {
	SchemaVersion   int               `json:"schema_version"`
	Revision        uint64            `json:"revision"`
	CatalogRevision uint64            `json:"catalog_revision"`
	Entries         []IMEIPoolEntry   `json:"entries"`
	Bindings        []IMEILineBinding `json:"bindings"`
	Unpooled        []IMEILineBinding `json:"unpooled"`
}

func (entry *IMEIPoolEntry) normalizeAndValidate() error {
	if entry == nil {
		return errors.New("IMEI pool entry is nil")
	}
	entry.ID = strings.TrimSpace(entry.ID)
	entry.Name = strings.TrimSpace(entry.Name)
	entry.IMEI = strings.TrimSpace(entry.IMEI)
	entry.Notes = strings.TrimSpace(entry.Notes)
	if entry.SchemaVersion == 0 {
		entry.SchemaVersion = IMEIPoolSchemaVersion
	}
	if entry.SchemaVersion != IMEIPoolSchemaVersion || !validIdentifier(entry.ID) ||
		entry.Name == "" || len(entry.Name) > 256 || containsControl(entry.Name) ||
		!digitsBetween(entry.IMEI, 15, 15) || len(entry.Notes) > 2048 || !validIMEINotes(entry.Notes) {
		return errors.New("IMEI pool entry is invalid")
	}
	return nil
}

func validIMEINotes(value string) bool {
	for _, char := range value {
		if char < 0x20 && char != '\n' && char != '\t' || char == 0x7f {
			return false
		}
	}
	return true
}

func (store *Store) IMEIPoolSnapshot() (IMEIPoolSnapshot, error) {
	result := IMEIPoolSnapshot{
		SchemaVersion: IMEIPoolSchemaVersion, Entries: []IMEIPoolEntry{},
		Bindings: []IMEILineBinding{}, Unpooled: []IMEILineBinding{},
	}
	err := store.db.View(func(tx *bolt.Tx) error {
		metadata := tx.Bucket(metadataBucket)
		result.Revision = bytesUint64(metadata.Get(imeiPoolRevisionKey))
		result.CatalogRevision = bytesUint64(metadata.Get(revisionKey))
		byIMEI := make(map[string]string)
		if err := tx.Bucket(imeiPoolEntriesBucket).ForEach(func(_, payload []byte) error {
			entry, err := decodeIMEIPoolEntry(payload)
			if err != nil {
				return err
			}
			if prior := byIMEI[entry.IMEI]; prior != "" && prior != entry.ID {
				return errors.New("stored IMEI pool index is ambiguous")
			}
			byIMEI[entry.IMEI] = entry.ID
			if indexed := tx.Bucket(imeiPoolValuesBucket).Get([]byte(entry.IMEI)); indexed == nil || string(indexed) != entry.ID {
				return errors.New("stored IMEI pool index is corrupt")
			}
			result.Entries = append(result.Entries, entry)
			return nil
		}); err != nil {
			return err
		}
		if err := tx.Bucket(imeiPoolValuesBucket).ForEach(func(imei, owner []byte) error {
			payload := tx.Bucket(imeiPoolEntriesBucket).Get(owner)
			entry, err := decodeIMEIPoolEntry(payload)
			if err != nil || entry.ID != string(owner) || entry.IMEI != string(imei) {
				return errors.New("stored IMEI pool index is corrupt")
			}
			return nil
		}); err != nil {
			return err
		}
		if err := tx.Bucket(pendingIMEIBindingsBucket).ForEach(func(cardID, entryID []byte) error {
			entry, err := pendingIMEIEntry(tx, string(cardID))
			if err != nil {
				return err
			}
			if tx.Bucket(cardsBucket).Get(cardID) != nil {
				return errors.New("stored IMEI binding has two owners")
			}
			result.Bindings = append(result.Bindings, IMEILineBinding{EntryID: string(entryID), CardID: string(cardID), IMEI: entry.IMEI})
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket(linesBucket).ForEach(func(_, payload []byte) error {
			line, err := decodeCatalogLine(payload)
			if err != nil {
				return err
			}
			if line.SIM.IMEI == "" {
				return nil
			}
			binding := IMEILineBinding{
				EntryID: byIMEI[line.SIM.IMEI], LineID: line.ID, LineName: line.Name,
				CardID: line.CardID, IMEI: line.SIM.IMEI,
			}
			if binding.EntryID == "" {
				result.Unpooled = append(result.Unpooled, binding)
			} else {
				result.Bindings = append(result.Bindings, binding)
			}
			return nil
		})
	})
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].ID < result.Entries[j].ID })
	sortIMEILineBindings(result.Bindings)
	sortIMEILineBindings(result.Unpooled)
	return result, err
}

func sortIMEILineBindings(values []IMEILineBinding) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].LineID != values[j].LineID {
			return values[i].LineID < values[j].LineID
		}
		if values[i].CardID != values[j].CardID {
			return values[i].CardID < values[j].CardID
		}
		return values[i].IMEI < values[j].IMEI
	})
}

func (store *Store) PutIMEIPoolEntryExpected(input IMEIPoolEntry,
	expectedRevision uint64) (IMEIPoolEntry, uint64, bool, error) {
	entry := input
	if err := entry.normalizeAndValidate(); err != nil {
		return IMEIPoolEntry{}, 0, false, err
	}
	result, revision, changed := IMEIPoolEntry{}, uint64(0), false
	err := store.db.Update(func(tx *bolt.Tx) error {
		metadata := tx.Bucket(metadataBucket)
		revision = bytesUint64(metadata.Get(imeiPoolRevisionKey))
		if revision != expectedRevision {
			return ErrIMEIPoolRevision
		}
		entries, values := tx.Bucket(imeiPoolEntriesBucket), tx.Bucket(imeiPoolValuesBucket)
		var prior IMEIPoolEntry
		if payload := entries.Get([]byte(entry.ID)); payload != nil {
			var err error
			prior, err = decodeIMEIPoolEntry(payload)
			if err != nil {
				return err
			}
			if prior == entry {
				if indexed := values.Get([]byte(prior.IMEI)); indexed == nil || string(indexed) != prior.ID {
					return errors.New("stored IMEI pool index is corrupt")
				}
				result = prior
				return nil
			}
			if prior.IMEI != entry.IMEI {
				used, err := imeiUsedByLine(tx, prior.IMEI)
				if err != nil {
					return err
				}
				if used {
					return ErrIMEIValueInUse
				}
			}
			if indexed := values.Get([]byte(prior.IMEI)); indexed == nil || string(indexed) != prior.ID {
				return errors.New("stored IMEI pool index is corrupt")
			}
		}
		if owner := values.Get([]byte(entry.IMEI)); owner != nil {
			if string(owner) != entry.ID {
				return ErrIMEIValueExists
			}
			if prior.ID == "" {
				return errors.New("stored IMEI pool index is corrupt")
			}
		}
		if prior.IMEI != "" && prior.IMEI != entry.IMEI {
			if err := values.Delete([]byte(prior.IMEI)); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if err := entries.Put([]byte(entry.ID), payload); err != nil {
			return err
		}
		if err := values.Put([]byte(entry.IMEI), []byte(entry.ID)); err != nil {
			return err
		}
		revision++
		if err := metadata.Put(imeiPoolRevisionKey, uint64Bytes(revision)); err != nil {
			return err
		}
		result, changed = entry, true
		return nil
	})
	return result, revision, changed, err
}

func (store *Store) DeleteIMEIPoolEntryExpected(entryID string,
	expectedRevision uint64) (uint64, error) {
	entryID = strings.TrimSpace(entryID)
	if !validIdentifier(entryID) {
		return 0, errors.New("IMEI pool entry ID is invalid")
	}
	revision := uint64(0)
	err := store.db.Update(func(tx *bolt.Tx) error {
		metadata := tx.Bucket(metadataBucket)
		revision = bytesUint64(metadata.Get(imeiPoolRevisionKey))
		if revision != expectedRevision {
			return ErrIMEIPoolRevision
		}
		entries := tx.Bucket(imeiPoolEntriesBucket)
		payload := entries.Get([]byte(entryID))
		if payload == nil {
			return ErrIMEIEntryNotFound
		}
		entry, err := decodeIMEIPoolEntry(payload)
		if err != nil {
			return err
		}
		if indexed := tx.Bucket(imeiPoolValuesBucket).Get([]byte(entry.IMEI)); indexed == nil || string(indexed) != entry.ID {
			return errors.New("stored IMEI pool index is corrupt")
		}
		used, err := imeiUsedByLine(tx, entry.IMEI)
		if err != nil {
			return err
		}
		if used {
			return ErrIMEIValueInUse
		}
		if err := entries.Delete([]byte(entryID)); err != nil {
			return err
		}
		if err := tx.Bucket(imeiPoolValuesBucket).Delete([]byte(entry.IMEI)); err != nil {
			return err
		}
		revision++
		return metadata.Put(imeiPoolRevisionKey, uint64Bytes(revision))
	})
	return revision, err
}

func (store *Store) BindIMEIExpected(entryID, lineID, expectedCardID string,
	expectedPoolRevision, expectedCatalogRevision uint64) (Line, uint64, uint64, bool, error) {
	return store.changeIMEIBinding(entryID, lineID, expectedCardID,
		expectedPoolRevision, expectedCatalogRevision, true, false)
}

func (store *Store) UnbindIMEIExpected(entryID, lineID, expectedCardID string,
	expectedPoolRevision, expectedCatalogRevision uint64) (Line, uint64, uint64, bool, error) {
	return store.changeIMEIBinding(entryID, lineID, expectedCardID,
		expectedPoolRevision, expectedCatalogRevision, false, false)
}

// Card bindings do not create a line or perform hardware work. The card index
// resolves an existing line inside the same revision-checked transaction.
// Restores ec620942 config.py's pre-line ICCID binding, without draft activation.
func (store *Store) BindIMEICardExpected(entryID, cardID string,
	expectedPoolRevision, expectedCatalogRevision uint64) (Line, uint64, uint64, bool, error) {
	return store.changeIMEIBinding(entryID, "", cardID, expectedPoolRevision, expectedCatalogRevision, true, true)
}

func (store *Store) UnbindIMEICardExpected(entryID, cardID string,
	expectedPoolRevision, expectedCatalogRevision uint64) (Line, uint64, uint64, bool, error) {
	return store.changeIMEIBinding(entryID, "", cardID, expectedPoolRevision, expectedCatalogRevision, false, true)
}

func (store *Store) changeIMEIBinding(entryID, lineID, expectedCardID string,
	expectedPoolRevision, expectedCatalogRevision uint64, bind, byCard bool) (Line, uint64, uint64, bool, error) {
	entryID, lineID = strings.TrimSpace(entryID), strings.TrimSpace(lineID)
	if !validIdentifier(entryID) || (!byCard && !validIdentifier(lineID)) || !digitsBetween(expectedCardID, 4, 32) {
		return Line{}, 0, 0, false, errors.New("IMEI binding identity is invalid")
	}
	result, poolRevision, catalogRevision, changed := Line{}, uint64(0), uint64(0), false
	err := store.db.Update(func(tx *bolt.Tx) error {
		metadata := tx.Bucket(metadataBucket)
		poolRevision = bytesUint64(metadata.Get(imeiPoolRevisionKey))
		catalogRevision = bytesUint64(metadata.Get(revisionKey))
		if poolRevision != expectedPoolRevision {
			return ErrIMEIPoolRevision
		}
		if catalogRevision != expectedCatalogRevision {
			return ErrRevision
		}
		entryPayload := tx.Bucket(imeiPoolEntriesBucket).Get([]byte(entryID))
		if entryPayload == nil {
			return ErrIMEIEntryNotFound
		}
		entry, err := decodeIMEIPoolEntry(entryPayload)
		if err != nil {
			return err
		}
		if indexed := tx.Bucket(imeiPoolValuesBucket).Get([]byte(entry.IMEI)); indexed == nil || string(indexed) != entry.ID {
			return errors.New("stored IMEI pool index is corrupt")
		}
		if byCard {
			lineID = string(tx.Bucket(cardsBucket).Get([]byte(expectedCardID)))
		}
		if byCard && lineID == "" {
			if err := tx.Bucket(operationBucket).ForEach(func(_, payload []byte) error {
				var receipt OperationReceipt
				if json.Unmarshal(payload, &receipt) != nil || receipt.Validate() != nil {
					return errors.New("stored operation receipt is corrupt")
				}
				if receipt.CardID == expectedCardID && (receipt.Kind == OperationProvision || receipt.Kind == OperationReprovision) {
					switch receipt.State {
					case OperationPrepared, OperationCatalogCommitted, OperationInProgress, OperationUnknown:
						return ErrLineOperationActive
					}
				}
				return nil
			}); err != nil {
				return err
			}
			pending := tx.Bucket(pendingIMEIBindingsBucket)
			prior := string(pending.Get([]byte(expectedCardID)))
			if prior != "" {
				if _, err := pendingIMEIEntry(tx, expectedCardID); err != nil {
					return err
				}
			}
			if !bind && prior != "" && prior != entryID {
				return ErrIMEIBinding
			}
			if bind && prior == entryID || !bind && prior == "" {
				return nil
			}
			if bind {
				err = pending.Put([]byte(expectedCardID), []byte(entryID))
			} else {
				err = pending.Delete([]byte(expectedCardID))
			}
			if err != nil {
				return err
			}
			poolRevision++
			catalogRevision++
			changed = true
			if err := metadata.Put(imeiPoolRevisionKey, uint64Bytes(poolRevision)); err != nil {
				return err
			}
			return metadata.Put(revisionKey, uint64Bytes(catalogRevision))
		}
		linePayload := tx.Bucket(linesBucket).Get([]byte(lineID))
		if linePayload == nil {
			return ErrNotFound
		}
		line, err := decodeCatalogLine(linePayload)
		if err != nil {
			return err
		}
		if line.CardID != expectedCardID {
			return ErrCardInUse
		}
		if tx.Bucket(pendingIMEIBindingsBucket).Get([]byte(expectedCardID)) != nil {
			return errors.New("stored IMEI binding has two owners")
		}
		active, err := activeProvisionOperation(tx.Bucket(operationBucket), lineID, "")
		if err != nil {
			return err
		}
		deleting, err := activeDeletionOperation(tx.Bucket(deletionOperationBucket), lineID)
		if err != nil {
			return err
		}
		if active || deleting {
			return ErrLineOperationActive
		}
		desired := entry.IMEI
		if !bind {
			if line.SIM.IMEI != "" && line.SIM.IMEI != entry.IMEI {
				return ErrIMEIBinding
			}
			desired = ""
		}
		if line.SIM.IMEI == desired {
			result = line
			return nil
		}
		line.SIM.IMEI = desired
		encoded, err := json.Marshal(line)
		if err != nil {
			return err
		}
		if err := tx.Bucket(linesBucket).Put([]byte(line.ID), encoded); err != nil {
			return err
		}
		poolRevision++
		catalogRevision++
		if err := metadata.Put(imeiPoolRevisionKey, uint64Bytes(poolRevision)); err != nil {
			return err
		}
		if err := metadata.Put(revisionKey, uint64Bytes(catalogRevision)); err != nil {
			return err
		}
		result, changed = line, true
		return nil
	})
	return cloneLine(result), poolRevision, catalogRevision, changed, err
}

func decodeIMEIPoolEntry(payload []byte) (IMEIPoolEntry, error) {
	var entry IMEIPoolEntry
	if json.Unmarshal(payload, &entry) != nil || entry.normalizeAndValidate() != nil {
		return IMEIPoolEntry{}, errors.New("stored IMEI pool entry is corrupt")
	}
	return entry, nil
}

func decodeCatalogLine(payload []byte) (Line, error) {
	var line Line
	if json.Unmarshal(payload, &line) != nil || line.normalizeAndValidate() != nil {
		return Line{}, errors.New("stored line is corrupt")
	}
	return line, nil
}

func imeiUsedByLine(tx *bolt.Tx, imei string) (bool, error) {
	used := false
	if err := tx.Bucket(pendingIMEIBindingsBucket).ForEach(func(cardID, _ []byte) error {
		entry, err := pendingIMEIEntry(tx, string(cardID))
		if err != nil {
			return err
		}
		used = used || entry.IMEI == imei
		return nil
	}); err != nil || used {
		return used, err
	}
	err := tx.Bucket(linesBucket).ForEach(func(_, payload []byte) error {
		line, err := decodeCatalogLine(payload)
		if err != nil {
			return err
		}
		if line.SIM.IMEI == imei {
			used = true
			return errIMEIFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, errIMEIFound) {
		return false, err
	}
	return used, nil
}

func pendingIMEIEntry(tx *bolt.Tx, cardID string) (IMEIPoolEntry, error) {
	entryID := tx.Bucket(pendingIMEIBindingsBucket).Get([]byte(cardID))
	if entryID == nil {
		return IMEIPoolEntry{}, nil
	}
	entry, err := decodeIMEIPoolEntry(tx.Bucket(imeiPoolEntriesBucket).Get(entryID))
	if err != nil || entry.ID != string(entryID) || !digitsBetween(cardID, 4, 32) ||
		string(tx.Bucket(imeiPoolValuesBucket).Get([]byte(entry.IMEI))) != entry.ID {
		return IMEIPoolEntry{}, errors.New("stored pending IMEI binding is corrupt")
	}
	return entry, nil
}

// Consume only while publishing the line in this transaction. Hardware provision
// must already carry the same IMEI: never silently change an admitted command.
func consumePendingIMEI(tx *bolt.Tx, line *Line, hardwareProvision bool) error {
	entry, err := pendingIMEIEntry(tx, line.CardID)
	if err != nil || entry.ID == "" {
		return err
	}
	if hardwareProvision && line.SIM.IMEI != entry.IMEI {
		return ErrIMEIBinding
	}
	line.SIM.IMEI = entry.IMEI
	return tx.Bucket(pendingIMEIBindingsBucket).Delete([]byte(line.CardID))
}

// Claim consumes pending identity, but later provision must not overwrite the
// resulting pool binding. Unpooled imported/default identities keep their path.
func checkProvisionIMEI(tx *bolt.Tx, current Line, cardID, imei string) error {
	if current.CardID == cardID && current.SIM.IMEI != imei &&
		tx.Bucket(imeiPoolValuesBucket).Get([]byte(current.SIM.IMEI)) != nil {
		return ErrIMEIBinding
	}
	return nil
}
