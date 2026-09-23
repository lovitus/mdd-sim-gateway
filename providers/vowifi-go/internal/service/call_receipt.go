package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	bolt "go.etcd.io/bbolt"
)

func callReceiptKey(scope string, input vowifiipc.CallReceiptRequest) string {
	return scope + "\x00" + input.CallID + "\x00" + input.OperationID + "\x00" + input.SessionID
}
func (store *MemoryOperationStore) SaveCallReceipt(scope string, receipt vowifiipc.CallReceipt) error {
	if scope == "" || receipt.Validate() != nil {
		return errors.New("invalid call receipt")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.terminals == nil {
		store.terminals = map[string]vowifiipc.CallReceipt{}
	}
	key := callReceiptKey(scope, receipt.CallReceiptRequest)
	if _, found := store.terminals[key]; !found {
		store.terminals[key] = receipt
	}
	return nil
}
func (store *MemoryOperationStore) LookupCallReceipt(scope string, input vowifiipc.CallReceiptRequest) (vowifiipc.CallReceipt, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.terminals[callReceiptKey(scope, input)]
	return record, found, nil
}
func (store *BoltOperationStore) SaveCallReceipt(scope string, receipt vowifiipc.CallReceipt) error {
	if scope == "" || receipt.Validate() != nil {
		return errors.New("invalid call receipt")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(callTerminalBucket)
		key := []byte(callReceiptKey(scope, receipt.CallReceiptRequest))
		if bucket.Get(key) != nil {
			return nil
		}
		payload, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		return bucket.Put(key, payload)
	})
}
func (store *BoltOperationStore) LookupCallReceipt(scope string, input vowifiipc.CallReceiptRequest) (vowifiipc.CallReceipt, bool, error) {
	var result vowifiipc.CallReceipt
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(callTerminalBucket).Get([]byte(callReceiptKey(scope, input)))
		if value == nil {
			return nil
		}
		if err := json.Unmarshal(value, &result); err != nil {
			return err
		}
		if err := result.Validate(); err != nil {
			return err
		}
		found = true
		return nil
	})
	return result, found, err
}
func (backend *Backend) persistCallTerminalLocked(active *activeVoiceCall, source string) error {
	if active.terminalSource == "" {
		active.terminalSource = source
	}
	source = active.terminalSource
	return backend.operations.SaveCallReceipt(backend.messageScope, vowifiipc.CallReceipt{CallReceiptRequest: vowifiipc.CallReceiptRequest{CallID: active.request.CallID, OperationID: active.request.OperationID, SessionID: callMediaSessionID(active.request.MediaSessionID, active.request.CallID)}, LineID: backend.lineID, ProviderID: backend.providerID, ProcessGeneration: backend.generation, ConfirmedAt: time.Now().UTC(), Source: source})
}
func (backend *Backend) CallReceipt(_ context.Context, input vowifiipc.CallReceiptRequest) (vowifiipc.CallReceipt, error) {
	if err := input.Validate(); err != nil {
		return vowifiipc.CallReceipt{}, err
	}
	backend.mu.Lock()
	var retired *activeVoiceCall
	defer func() {
		backend.mu.Unlock()
		if retired != nil {
			retired.session.EndStream("confirmed terminal receipt")
		}
	}()
	result, found, err := backend.operations.LookupCallReceipt(backend.messageScope, input)
	if err != nil {
		return result, err
	}
	if !found {
		if active := backend.activeCall; active != nil && active.request.CallID == input.CallID && active.request.OperationID == input.OperationID && callMediaSessionID(active.request.MediaSessionID, active.request.CallID) == input.SessionID {
			if active.terminationConfirmed {
				if err := backend.persistCallTerminalLocked(active, "confirmed_end"); err != nil {
					return result, err
				}
				result, _, err = backend.operations.LookupCallReceipt(backend.messageScope, input)
				if err == nil {
					backend.finishCallLocked(active)
					retired = active
				}
				return result, err
			}
			return result, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "call_active", Layer: "call"}
		}
		return result, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotFound, Code: "call_terminal_unavailable", Layer: "call"}
	}
	return result, nil
}
