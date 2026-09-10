package events

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	bolt "go.etcd.io/bbolt"
	"time"
)

// Stored only in the existing mode-0600 database. Never return this struct from
// an inventory/list endpoint; backups inherit its sensitive-data classification.
type EUICCDownloadRecovery struct {
	CodeSource          string    `json:"code_source,omitempty"`
	EID                 string    `json:"eid"`
	OperationID         string    `json:"operation_id"`
	RequestHash         string    `json:"request_hash,omitempty"`
	RetainCodes         bool      `json:"retain_codes"`
	ActivationCode      string    `json:"activation_code,omitempty"`
	ConfirmationCode    string    `json:"confirmation_code,omitempty"`
	ICCID               string    `json:"iccid,omitempty"`
	ProfileName         string    `json:"profile_name,omitempty"`
	ServiceProviderName string    `json:"service_provider_name,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type EUICCRecoverySummary struct {
	CodeSource            string    `json:"code_source,omitempty"`
	DownloadedAt          time.Time `json:"downloaded_at"`
	DownloadOperationID   string    `json:"download_operation_id"`
	ProfileName           string    `json:"profile_name,omitempty"`
	ServiceProviderName   string    `json:"service_provider_name,omitempty"`
	ActivationCodeSaved   bool      `json:"activation_code_saved"`
	ConfirmationCodeSaved bool      `json:"confirmation_code_saved"`
}

func downloadRecoveryKey(eid, operation string) []byte {
	return []byte("euicc-download-recovery-v1\x00" + eid + "\x00" + operation)
}
func recoveryProfileKey(eid, iccid string) []byte {
	return []byte("euicc-recovery-profile-v1\x00" + eid + "\x00" + iccid)
}

func (store *BoltStore) SaveEUICCDownloadRecovery(command agentlink.EUICCDownloadCommand, retain bool) error {
	if command.Validate() != nil || command.Action != agentlink.EUICCDownloadStart {
		return errors.New("invalid download recovery identity")
	}
	raw, _ := json.Marshal([]string{command.EID, command.ActivationCode, command.ConfirmationCode, command.IMEI})
	digest := sha256.Sum256(raw)
	r := EUICCDownloadRecovery{EID: command.EID, OperationID: command.OperationID, RequestHash: hex.EncodeToString(digest[:]), RetainCodes: retain, CreatedAt: time.Now().UTC()}
	if retain {
		r.CodeSource = "download_request"
		r.ActivationCode = command.ActivationCode
		r.ConfirmationCode = command.ConfirmationCode
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMetadata)
		key := downloadRecoveryKey(r.EID, r.OperationID)
		if data := b.Get(key); data != nil {
			var prior EUICCDownloadRecovery
			if err := json.Unmarshal(data, &prior); err != nil {
				return err
			}
			if prior.RequestHash != r.RequestHash || prior.RetainCodes != retain {
				return errors.New("download recovery intent changed")
			}
			return nil
		}
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return b.Put(key, data)
	})
}

func (store *BoltStore) BindEUICCDownloadRecovery(eid, operation string, metadata agentlink.EUICCDownloadMetadata, startedAt time.Time) error {
	if _, err := euiccDeletionKey(eid, metadata.ICCID); err != nil {
		return err
	}
	if !validRecoveryLine(operation) || len(metadata.ProfileName) > 256 || len(metadata.ServiceProviderName) > 256 {
		return errors.New("invalid download metadata")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMetadata)
		key := downloadRecoveryKey(eid, operation)
		r := EUICCDownloadRecovery{EID: eid, OperationID: operation, CreatedAt: time.Now().UTC()}
		if !startedAt.IsZero() {
			r.CreatedAt = startedAt
		}
		if data := b.Get(key); data != nil {
			if err := json.Unmarshal(data, &r); err != nil {
				return err
			}
			if r.ICCID != "" && r.ICCID != metadata.ICCID {
				return errors.New("download profile identity changed")
			}
		}
		if r.ProfileName != "" && metadata.ProfileName != "" && r.ProfileName != metadata.ProfileName {
			return errors.New("download profile name changed")
		}
		if r.ServiceProviderName != "" && metadata.ServiceProviderName != "" && r.ServiceProviderName != metadata.ServiceProviderName {
			return errors.New("download provider changed")
		}
		r.ICCID = metadata.ICCID
		if r.ProfileName == "" {
			r.ProfileName = metadata.ProfileName
		}
		if r.ServiceProviderName == "" {
			r.ServiceProviderName = metadata.ServiceProviderName
		}
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err = b.Put(key, data); err != nil {
			return err
		}
		if previous := b.Get(recoveryProfileKey(eid, r.ICCID)); previous != nil && string(previous) != operation {
			var prior EUICCDownloadRecovery
			if err := json.Unmarshal(b.Get(downloadRecoveryKey(eid, string(previous))), &prior); err != nil {
				return err
			}
			if prior.CreatedAt.After(r.CreatedAt) {
				return nil
			}
		}
		return b.Put(recoveryProfileKey(eid, r.ICCID), []byte(operation))
	})
}

func (store *BoltStore) EUICCProfileRecovery(eid, iccid string) (EUICCDownloadRecovery, bool, error) {
	if _, err := euiccDeletionKey(eid, iccid); err != nil {
		return EUICCDownloadRecovery{}, false, err
	}
	var record EUICCDownloadRecovery
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMetadata)
		operation := b.Get(recoveryProfileKey(eid, iccid))
		if operation == nil {
			return nil
		}
		if err := json.Unmarshal(b.Get(downloadRecoveryKey(eid, string(operation))), &record); err != nil {
			return err
		}
		if record.EID != eid || record.ICCID != iccid {
			return errors.New("recovery identity mismatch")
		}
		found = true
		return nil
	})
	return record, found, err
}

func (r EUICCDownloadRecovery) Summary() EUICCRecoverySummary {
	return EUICCRecoverySummary{CodeSource: r.CodeSource, DownloadedAt: r.CreatedAt, DownloadOperationID: r.OperationID, ProfileName: r.ProfileName, ServiceProviderName: r.ServiceProviderName, ActivationCodeSaved: r.ActivationCode != "", ConfirmationCodeSaved: r.ConfirmationCode != ""}
}

// Explicit operator recovery is for already-completed downloads, never a retry.
func (store *BoltStore) RestoreEUICCRecoveryCodes(command agentlink.EUICCDownloadCommand, iccid string) error {
	if command.Validate() != nil || command.Action != agentlink.EUICCDownloadStart {
		return errors.New("invalid recovery codes")
	}
	raw, _ := json.Marshal([]string{command.EID, command.ActivationCode, command.ConfirmationCode, command.IMEI})
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	return store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMetadata)
		key := downloadRecoveryKey(command.EID, command.OperationID)
		var record EUICCDownloadRecovery
		if err := json.Unmarshal(b.Get(key), &record); err != nil {
			return err
		}
		if record.ICCID == "" || record.ICCID != iccid || record.RequestHash != "" && record.RequestHash != digest {
			return errors.New("download recovery identity conflict")
		}
		if record.RetainCodes {
			if record.ActivationCode != command.ActivationCode || record.ConfirmationCode != command.ConfirmationCode {
				return errors.New("recovery codes already recorded")
			}
			return nil
		}
		record.RetainCodes = true
		record.ActivationCode = command.ActivationCode
		record.ConfirmationCode = command.ConfirmationCode
		record.RequestHash = digest
		record.CodeSource = "operator_recovered"
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return b.Put(key, data)
	})
}

func (store *BoltStore) EUICCDownloadRecoveryByOperation(eid, operation string) (EUICCDownloadRecovery, bool, error) {
	if (agentlink.EUICCDownloadCommand{EID: eid, OperationID: operation, Action: agentlink.EUICCDownloadStatus}).Validate() != nil {
		return EUICCDownloadRecovery{}, false, errors.New("invalid recovery lookup")
	}
	var record EUICCDownloadRecovery
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketMetadata).Get(downloadRecoveryKey(eid, operation))
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		found = true
		return nil
	})
	return record, found, err
}
