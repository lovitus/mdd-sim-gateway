package callhistory

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	bolt "go.etcd.io/bbolt"
)

var ErrRecoveryIdentity = errors.New("call recovery identity is unavailable or does not match")

// ValidateRecoveryDispatch binds a recovery-enabled lease to the actual paid start.
// Legacy calls without a recovery association retain their existing admission path.
func (store *Store) ValidateRecoveryDispatch(line, transport, call, operation, session, subject, card, generation string) error {
	if session == "" {
		session = call
	}
	return store.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket(recoverySessionsBucket).Get(recoverySessionKey(transport, session))
		indexed := id != nil
		if id == nil {
			id = recordKey(line, transport, call)
		}
		value := tx.Bucket(recoveryBucket).Get(id)
		if value == nil {
			if indexed {
				return ErrRecoveryIdentity
			}
			return nil
		}
		var record RecoveryRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if record.LineID != line || record.Transport != transport || record.CallID != call || record.OperationID != operation || record.SessionID != session || record.Subject != subject || record.CardID != card || record.ProviderGeneration != generation || !record.TerminalAt.IsZero() {
			return ErrRecoveryIdentity
		}
		return nil
	})
}

func recoverySessionKey(transport, session string) []byte {
	return []byte(transport + "\x00" + session)
}
func indexRecoverySessions(tx *bolt.Tx) error {
	index := tx.Bucket(recoverySessionsBucket)
	return tx.Bucket(recoveryBucket).ForEach(func(id, value []byte) error {
		var row RecoveryRecord
		if err := json.Unmarshal(value, &row); err != nil {
			return err
		}
		if row.SessionID == "" || row.Transport != "vowifi" && row.Transport != "cellular" {
			return ErrRecoveryIdentity
		}
		key := recoverySessionKey(row.Transport, row.SessionID)
		previous := index.Get(key)
		if previous != nil && !bytes.Equal(previous, id) {
			return ErrRecoveryIdentity
		}
		return index.Put(key, id)
	})
}

// RecoveryRecord is an authorization/receipt association, not a second call owner.
// It is deliberately not removed by deletion of user-visible call history.
type RecoveryRecord struct {
	LineID             string                `json:"line_id"`
	Transport          string                `json:"transport"`
	CardID             string                `json:"card_id"`
	CallID             string                `json:"call_id"`
	OperationID        string                `json:"operation_id"`
	SessionID          string                `json:"session_id"`
	Subject            string                `json:"subject"`
	KeyHash            string                `json:"key_hash"`
	ProviderID         string                `json:"provider_id,omitempty"`
	ProviderGeneration string                `json:"provider_generation,omitempty"`
	Target             agentlink.ModemTarget `json:"target,omitempty"`
	CreatedAt          time.Time             `json:"created_at"`
	TerminalAt         time.Time             `json:"terminal_at,omitempty"`
	TerminalSource     string                `json:"terminal_source,omitempty"`
}

func validRecoveryKey(key string) bool {
	return len(key) >= 64 && len(key) <= 256 && !strings.ContainsAny(key, "\r\n\x00")
}
func recoveryKeyHash(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

func (store *Store) BindRecovery(record RecoveryRecord, key string) error {
	if !validRecordIdentity(record.LineID, record.Transport, record.CallID, "out") || !validCardID(record.CardID) || record.OperationID == "" || len(record.OperationID) > 128 || record.SessionID == "" || record.Subject == "" || record.CreatedAt.IsZero() || !validRecoveryKey(key) || !record.TerminalAt.IsZero() || record.TerminalSource != "" {
		return ErrRecoveryIdentity
	}
	if record.Transport == "cellular" && (record.Target.AgentID == "" || record.Target.AttachmentID == "" || record.Target.EquipmentID == "" || record.Target.CardID != record.CardID) {
		return ErrRecoveryIdentity
	}
	if record.Transport == "vowifi" && (record.ProviderID == "" || record.ProviderGeneration == "") {
		return ErrRecoveryIdentity
	}
	record.KeyHash = recoveryKeyHash(key)
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recoveryBucket)
		id := recordKey(record.LineID, record.Transport, record.CallID)
		index := tx.Bucket(recoverySessionsBucket)
		sessionKey := recoverySessionKey(record.Transport, record.SessionID)
		if previous := index.Get(sessionKey); previous != nil && !bytes.Equal(previous, id) {
			return ErrRecoveryIdentity
		}
		if existing := bucket.Get(id); existing != nil {
			var old RecoveryRecord
			if err := json.Unmarshal(existing, &old); err != nil {
				return err
			}
			if old.KeyHash != record.KeyHash || old.OperationID != record.OperationID || old.SessionID != record.SessionID || old.Subject != record.Subject || old.CardID != record.CardID {
				return ErrRecoveryIdentity
			}
			return nil
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := bucket.Put(id, payload); err != nil {
			return err
		}
		return index.Put(sessionKey, id)
	})
}

func (store *Store) ReadRecovery(line, transport, call, operation, key string) (RecoveryRecord, error) {
	var result RecoveryRecord
	if !validRecordIdentity(line, transport, call, "out") || !validRecoveryKey(key) {
		return result, ErrRecoveryIdentity
	}
	err := store.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(recoveryBucket).Get(recordKey(line, transport, call))
		if value == nil {
			return ErrRecoveryIdentity
		}
		if err := json.Unmarshal(value, &result); err != nil {
			return err
		}
		if result.OperationID != operation || subtle.ConstantTimeCompare([]byte(result.KeyHash), []byte(recoveryKeyHash(key))) != 1 {
			return ErrRecoveryIdentity
		}
		return nil
	})
	return result, err
}

// ConfirmRecovery consumes proof from the existing hardware/protocol owner only.
// Generic history Status or the disappearance of an active call must never call it.
func (store *Store) ConfirmRecovery(expected RecoveryRecord, at time.Time, source string) error {
	if at.IsZero() || at.After(time.Now().Add(time.Minute)) ||
		!(expected.Transport == "cellular" && source == "agent_terminal_receipt" || expected.Transport == "vowifi" && (source == "provider_terminal_receipt" || source == "provider_rejection_receipt")) {
		return ErrRecoveryIdentity
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recoveryBucket)
		key := recordKey(expected.LineID, expected.Transport, expected.CallID)
		value := bucket.Get(key)
		if value == nil {
			return ErrRecoveryIdentity
		}
		var current RecoveryRecord
		if err := json.Unmarshal(value, &current); err != nil {
			return err
		}
		if current.KeyHash != expected.KeyHash || current.SessionID != expected.SessionID || current.OperationID != expected.OperationID || current.CardID != expected.CardID {
			return ErrRecoveryIdentity
		}
		if !current.TerminalAt.IsZero() {
			return nil
		}
		current.TerminalAt, current.TerminalSource = at.UTC(), source
		payload, err := json.Marshal(current)
		if err != nil {
			return err
		}
		return bucket.Put(key, payload)
	})
}
