package linecatalog

import (
	"encoding/json"
	"errors"

	bolt "go.etcd.io/bbolt"
)

// BackupExport is an offline recovery record, not an instruction to start a
// runtime or retry an operation. Derived indexes and authentication keys are
// excluded; all included fields come from one catalog read transaction.
type BackupExport struct {
	Snapshot
	ExportVersion         int                `json:"export_version"`
	RuntimeIntentRevision uint64             `json:"runtime_intent_revision"`
	RuntimeIntents        map[string]bool    `json:"runtime_intents"`
	RawModemRevision      uint64             `json:"raw_modem_revision"`
	RawModemBindings      []RawModemBinding  `json:"raw_modem_bindings"`
	IMEIPoolRevision      uint64             `json:"imei_pool_revision"`
	IMEIEntries           []IMEIPoolEntry    `json:"imei_pool_entries"`
	PendingIMEIBindings   map[string]string  `json:"pending_imei_bindings"`
	Operations            []OperationReceipt `json:"operations"`
	Deletions             []DeletionReceipt  `json:"deletions"`
	ImportReceipt         *ImportReceipt     `json:"import_receipt,omitempty"`
}

func (store *Store) Backup() ([]byte, error) {
	result := BackupExport{Snapshot: Snapshot{SchemaVersion: SchemaVersion}, ExportVersion: 2,
		RuntimeIntents: map[string]bool{}, PendingIMEIBindings: map[string]string{}}
	err := store.db.View(func(tx *bolt.Tx) error {
		metadata := tx.Bucket(metadataBucket)
		result.Revision = bytesUint64(metadata.Get(revisionKey))
		result.RuntimeIntentRevision = bytesUint64(metadata.Get(runtimeIntentRevisionKey))
		result.RawModemRevision = bytesUint64(metadata.Get(rawModemRevisionKey))
		result.IMEIPoolRevision = bytesUint64(metadata.Get(imeiPoolRevisionKey))
		var err error
		if result.Defaults, err = readProviderDefaults(tx); err != nil {
			return err
		}
		if result.Lines, err = backupRecords[Line](tx.Bucket(linesBucket)); err != nil {
			return err
		}
		for index := range result.Lines {
			line := &result.Lines[index]
			if err := line.normalizeAndValidate(); err != nil {
				return err
			}
			line.Deleted = tx.Bucket(lifecycleBucket).Get([]byte(line.ID)) != nil
			for index := range line.Network.APNProfiles {
				line.Network.APNProfiles[index].Password = ""
			}
		}
		if err := tx.Bucket(runtimeIntentsBucket).ForEach(func(key, value []byte) error {
			if len(value) != 1 || value[0] > 1 {
				return errors.New("invalid stored runtime intent")
			}
			result.RuntimeIntents[string(key)] = value[0] == 1
			return nil
		}); err != nil {
			return err
		}
		if result.RawModemBindings, err = backupRecords[RawModemBinding](tx.Bucket(rawModemBindingsBucket)); err != nil {
			return err
		}
		if result.IMEIEntries, err = backupRecords[IMEIPoolEntry](tx.Bucket(imeiPoolEntriesBucket)); err != nil {
			return err
		}
		if err := tx.Bucket(pendingIMEIBindingsBucket).ForEach(func(key, value []byte) error {
			if _, err := pendingIMEIEntry(tx, string(key)); err != nil {
				return err
			}
			result.PendingIMEIBindings[string(key)] = string(value)
			return nil
		}); err != nil {
			return err
		}
		if result.Operations, err = backupRecords[OperationReceipt](tx.Bucket(operationBucket)); err != nil {
			return err
		}
		if result.Deletions, err = backupRecords[DeletionReceipt](tx.Bucket(deletionOperationBucket)); err != nil {
			return err
		}
		if wire := metadata.Get(importKey); wire != nil {
			return json.Unmarshal(wire, &result.ImportReceipt)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func backupRecords[T any](bucket *bolt.Bucket) ([]T, error) {
	result := []T{}
	err := bucket.ForEach(func(_, wire []byte) error {
		var record T
		if err := json.Unmarshal(wire, &record); err != nil {
			return err
		}
		result = append(result, record)
		return nil
	})
	return result, err
}
