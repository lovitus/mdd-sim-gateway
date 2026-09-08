package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
	bolt "go.etcd.io/bbolt"
)

var ErrExitRecoveryRevision = errors.New("exit recovery revision changed")
var ErrExitRecoveryDeleted = errors.New("exit recovery line was permanently deleted")
var ErrExitRecoveryPending = errors.New("exit recovery selection is unresolved")

type ExitRecoverySnapshot struct {
	Revision uint64              `json:"revision"`
	Ledger   recovery.ExitLedger `json:"ledger"`
}

func exitRecoveryKey(lineID string) []byte { return []byte("exit-recovery-v1\x00" + lineID) }

func validRecoveryLine(lineID string) bool {
	return lineID != "" && strings.TrimSpace(lineID) == lineID && len(lineID) <= 256 && !strings.ContainsRune(lineID, 0)
}

func validExitRecoveryLedger(ledger recovery.ExitLedger) bool {
	if selection := ledger.Selection; selection != nil {
		if selection.Request.Validate() != nil || selection.Attempts < 0 ||
			(selection.State != "pending" && selection.State != "unknown" && selection.State != "applied" && selection.State != "canceled") {
			return false
		}
	}
	return ledger.Failures >= 0 && ledger.Strikes >= 0 && len(ledger.Tried) <= 128
}

func readExitRecovery(tx *bolt.Tx, lineID string) (ExitRecoverySnapshot, error) {
	var result ExitRecoverySnapshot
	if tx.Bucket(bucketPurgedLines).Get([]byte(lineID)) != nil {
		return result, ErrExitRecoveryDeleted
	}
	data := tx.Bucket(bucketMetadata).Get(exitRecoveryKey(lineID))
	if data == nil {
		return result, nil
	}
	if len(data) > 64<<10 || json.Unmarshal(data, &result) != nil || result.Revision == 0 || !validExitRecoveryLedger(result.Ledger) {
		return ExitRecoverySnapshot{}, errors.New("stored exit recovery state is invalid")
	}
	return result, nil
}

// ExitRecovery reads runtime evidence from the existing events database,
// never from the desired-state catalog. Missing state has revision zero.
func (store *BoltStore) ExitRecovery(lineID string) (ExitRecoverySnapshot, error) {
	if !validRecoveryLine(lineID) {
		return ExitRecoverySnapshot{}, errors.New("invalid recovery line")
	}
	var result ExitRecoverySnapshot
	err := store.db.View(func(tx *bolt.Tx) error { var err error; result, err = readExitRecovery(tx, lineID); return err })
	return result, err
}

// ActiveExitRecovery protects unresolved requests from destructive line cleanup.
func (store *BoltStore) ActiveExitRecovery(lineID string) (bool, error) {
	current, err := store.ExitRecovery(lineID)
	if errors.Is(err, ErrExitRecoveryDeleted) {
		return false, nil
	}
	return current.Ledger.Selection.Pending(), err
}

// PutExitRecoveryExpected requires generation/order validation by the caller.
// Duplicate decisions do not advance the revision or rewrite the state.
func (store *BoltStore) PutExitRecoveryExpected(lineID string, ledger recovery.ExitLedger, expected uint64) (ExitRecoverySnapshot, error) {
	if !validRecoveryLine(lineID) || !validExitRecoveryLedger(ledger) {
		return ExitRecoverySnapshot{}, errors.New("invalid exit recovery state")
	}
	value, err := json.Marshal(ledger)
	if err != nil || len(value) > 60<<10 {
		return ExitRecoverySnapshot{}, errors.New("exit recovery state exceeds limit")
	}
	var result ExitRecoverySnapshot
	err = store.db.Update(func(tx *bolt.Tx) error {
		current, err := readExitRecovery(tx, lineID)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return ErrExitRecoveryRevision
		}
		if err := putExitNotice(tx, ledger.Notice, lineID); err != nil {
			return err
		}
		previous, _ := json.Marshal(current.Ledger)
		if bytes.Equal(previous, value) {
			result = current
			return nil
		}
		if current.Revision == ^uint64(0) {
			return errors.New("exit recovery revision exhausted")
		}
		result = ExitRecoverySnapshot{Revision: current.Revision + 1, Ledger: ledger}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketMetadata).Put(exitRecoveryKey(lineID), payload)
	})
	return result, err
}
