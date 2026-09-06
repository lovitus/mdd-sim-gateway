package linecatalog

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrOperationExists       = errors.New("operation receipt already exists")
	ErrOperationNotFound     = errors.New("operation receipt not found")
	ErrOperationStateChanged = errors.New("operation receipt state changed")
	ErrOperationReused       = errors.New("operation id was reused with a different request")
	ErrLineOperationActive   = errors.New("another line operation is active")
)

// OperationSchemaVersion is the durable receipt schema used by future
// provision/reprovision transactions. Receipts deliberately contain no
// subscriber secrets; they only carry identity fences and sanitized outcomes.
const OperationSchemaVersion = 1

type OperationKind string

const (
	OperationClaim             OperationKind = "claim"
	OperationProvision         OperationKind = "provision"
	OperationReprovision       OperationKind = "reprovision"
	OperationProvisionReadback OperationKind = "provision_readback"
	OperationReaderReadback    OperationKind = "reader_readback"
	OperationReaderProvision   OperationKind = "reader_provision"
	OperationSIMPINStatus      OperationKind = "sim_pin_status"
	OperationSIMPIN            OperationKind = "sim_pin"
	OperationModemRecovery     OperationKind = "modem_recovery"
)

type OperationState string

const (
	OperationPrepared         OperationState = "prepared"
	OperationCatalogCommitted OperationState = "catalog_committed"
	OperationInProgress       OperationState = "in_progress"
	OperationSucceeded        OperationState = "succeeded"
	OperationFailed           OperationState = "failed"
	OperationUnknown          OperationState = "unknown"
	OperationReconciled       OperationState = "reconciled"
)

// OperationReceipt is intentionally a value object. Persistence and atomic
// catalog coupling belong to Store; callers must validate before writing it.
type OperationReceipt struct {
	SchemaVersion            int            `json:"schema_version"`
	OperationID              string         `json:"operation_id"`
	Kind                     OperationKind  `json:"kind"`
	State                    OperationState `json:"state"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
	RequestDigest            string         `json:"request_digest"`
	PreconditionDigest       string         `json:"precondition_digest,omitempty"`
	CandidateID              string         `json:"candidate_id,omitempty"`
	CandidateDigest          string         `json:"candidate_digest,omitempty"`
	ExpectedCatalogRevision  uint64         `json:"expected_catalog_revision"`
	CommittedCatalogRevision uint64         `json:"committed_catalog_revision,omitempty"`
	LineID                   string         `json:"line_id,omitempty"`
	CardID                   string         `json:"card_id,omitempty"`
	AgentID                  string         `json:"agent_id,omitempty"`
	ProcessGeneration        string         `json:"process_generation,omitempty"`
	AttachmentID             string         `json:"attachment_id,omitempty"`
	ReaderName               string         `json:"reader_name,omitempty"`
	EquipmentID              string         `json:"equipment_id,omitempty"`
	SIMSessionGeneration     string         `json:"sim_session_generation,omitempty"`
	Step                     string         `json:"step,omitempty"`
	OutcomeCode              string         `json:"outcome_code,omitempty"`
	ErrorCode                string         `json:"error_code,omitempty"`
	ErrorDetail              string         `json:"error_detail,omitempty"`
	AttemptCount             uint32         `json:"attempt_count"`
	ProviderGeneration       string         `json:"provider_generation,omitempty"`
	RuntimeGeneration        string         `json:"runtime_generation,omitempty"`
	EnableAfterSuccess       *bool          `json:"enable_after_success,omitempty"`
	ExistingLine             bool           `json:"existing_line,omitempty"`
	PINState                 string         `json:"pin_state,omitempty"`
	PINAttemptsRemaining     *uint32        `json:"pin_attempts_remaining,omitempty"`
}

// OperationStatus is the public, redacted projection of a durable receipt.
// Hardware identity fences remain private to the catalog and Agent protocols.
type OperationStatus struct {
	SchemaVersion            int            `json:"schema_version"`
	OperationID              string         `json:"operation_id"`
	Kind                     OperationKind  `json:"kind"`
	State                    OperationState `json:"state"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
	RequestDigest            string         `json:"request_digest"`
	CandidateID              string         `json:"candidate_id,omitempty"`
	ExpectedCatalogRevision  uint64         `json:"expected_catalog_revision"`
	CommittedCatalogRevision uint64         `json:"committed_catalog_revision,omitempty"`
	LineID                   string         `json:"line_id,omitempty"`
	Step                     string         `json:"step,omitempty"`
	OutcomeCode              string         `json:"outcome_code,omitempty"`
	ErrorCode                string         `json:"error_code,omitempty"`
	ErrorDetail              string         `json:"error_detail,omitempty"`
	AttemptCount             uint32         `json:"attempt_count"`
}

func (receipt OperationReceipt) PublicStatus() OperationStatus {
	return OperationStatus{SchemaVersion: receipt.SchemaVersion, OperationID: receipt.OperationID,
		Kind: receipt.Kind, State: receipt.State, CreatedAt: receipt.CreatedAt, UpdatedAt: receipt.UpdatedAt,
		RequestDigest: receipt.RequestDigest, CandidateID: receipt.CandidateID,
		ExpectedCatalogRevision: receipt.ExpectedCatalogRevision, CommittedCatalogRevision: receipt.CommittedCatalogRevision,
		LineID: receipt.LineID, Step: receipt.Step, OutcomeCode: receipt.OutcomeCode,
		ErrorCode: receipt.ErrorCode, ErrorDetail: receipt.ErrorDetail, AttemptCount: receipt.AttemptCount}
}

func (receipt OperationReceipt) Validate() error {
	if receipt.SchemaVersion != OperationSchemaVersion {
		return errors.New("unsupported operation receipt schema")
	}
	if !validOperationID(receipt.OperationID) {
		return errors.New("invalid operation id")
	}
	if receipt.Kind != OperationClaim && receipt.Kind != OperationProvision && receipt.Kind != OperationReprovision &&
		receipt.Kind != OperationProvisionReadback && receipt.Kind != OperationReaderReadback && receipt.Kind != OperationReaderProvision &&
		receipt.Kind != OperationSIMPINStatus && receipt.Kind != OperationSIMPIN &&
		receipt.Kind != OperationModemRecovery {
		return errors.New("invalid operation kind")
	}
	switch receipt.State {
	case OperationPrepared, OperationCatalogCommitted, OperationInProgress, OperationSucceeded,
		OperationFailed, OperationUnknown, OperationReconciled:
	default:
		return errors.New("invalid operation state")
	}
	if receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() || receipt.UpdatedAt.Before(receipt.CreatedAt) {
		return errors.New("invalid operation timestamps")
	}
	if !sha256Digest(receipt.RequestDigest) {
		return errors.New("invalid request digest")
	}
	if receipt.PreconditionDigest != "" && !sha256Digest(receipt.PreconditionDigest) {
		return errors.New("invalid precondition digest")
	}
	if receipt.CandidateDigest != "" && !sha256Digest(receipt.CandidateDigest) {
		return errors.New("invalid candidate digest")
	}
	if receipt.AttemptCount == 0 {
		return errors.New("operation attempt count is zero")
	}
	if len(receipt.ErrorCode) > 128 || len(receipt.ErrorDetail) > 1024 || len(receipt.Step) > 128 || len(receipt.OutcomeCode) > 128 {
		return errors.New("operation diagnostic is too large")
	}
	if receipt.PINState != "" && receipt.PINState != "verified" && receipt.PINState != "pin_required" && receipt.PINState != "retry_counter" &&
		receipt.PINState != "blocked" || receipt.PINAttemptsRemaining != nil && *receipt.PINAttemptsRemaining > 255 {
		return errors.New("operation PIN status is invalid")
	}
	return nil
}

// GetOperation returns a durable receipt from the same database that owns the
// line catalog. A missing receipt is distinct from a corrupt one.
func (store *Store) GetOperation(operationID string) (OperationReceipt, bool, error) {
	var receipt OperationReceipt
	if !validOperationID(operationID) {
		return OperationReceipt{}, false, errors.New("invalid operation id")
	}
	err := store.db.View(func(transaction *bolt.Tx) error {
		payload := transaction.Bucket(operationBucket).Get([]byte(operationID))
		if payload == nil {
			return nil
		}
		if err := json.Unmarshal(payload, &receipt); err != nil {
			return errors.New("stored operation receipt is corrupt")
		}
		return receipt.Validate()
	})
	if err != nil {
		return OperationReceipt{}, false, err
	}
	return receipt, !receipt.CreatedAt.IsZero(), nil
}

// LookupOperation compares the caller's request digest before any side
// effect. A matching receipt is safe to replay; a mismatched digest is an
// operation identity conflict.
func (store *Store) LookupOperation(operationID, requestDigest string) (OperationReceipt, bool, error) {
	if !sha256Digest(requestDigest) {
		return OperationReceipt{}, false, errors.New("invalid request digest")
	}
	receipt, found, err := store.GetOperation(operationID)
	if err != nil || !found {
		return receipt, found, err
	}
	if receipt.RequestDigest != requestDigest {
		return OperationReceipt{}, true, ErrOperationReused
	}
	return receipt, true, nil
}

// PutOperation stores one validated receipt. It deliberately refuses to
// overwrite an existing operation ID; replay/conflict decisions must compare
// digests before a caller chooses an explicit state transition.
func (store *Store) PutOperation(receipt OperationReceipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return store.db.Update(func(transaction *bolt.Tx) error {
		bucket := transaction.Bucket(operationBucket)
		if bucket.Get([]byte(receipt.OperationID)) != nil {
			return ErrOperationExists
		}
		return bucket.Put([]byte(receipt.OperationID), payload)
	})
}

// UpdateOperationCAS advances one receipt only when its current state and
// request identity still match. This is the only supported overwrite path.
func (store *Store) UpdateOperationCAS(receipt OperationReceipt, expectedState OperationState, expectedDigest string) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if expectedState == "" || !sha256Digest(expectedDigest) {
		return errors.New("invalid operation compare-and-set precondition")
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return store.db.Update(func(transaction *bolt.Tx) error {
		bucket := transaction.Bucket(operationBucket)
		currentPayload := bucket.Get([]byte(receipt.OperationID))
		if currentPayload == nil {
			return ErrOperationNotFound
		}
		var current OperationReceipt
		if err := json.Unmarshal(currentPayload, &current); err != nil || current.Validate() != nil {
			return errors.New("stored operation receipt is corrupt")
		}
		if current.State != expectedState || current.RequestDigest != expectedDigest {
			return ErrOperationStateChanged
		}
		return bucket.Put([]byte(receipt.OperationID), payload)
	})
}

// ReconcileProvisionOperation atomically restores the intended enabled state
// and advances an unknown provision receipt after independent hardware proof.
func (store *Store) ReconcileProvisionOperation(input Line, operationID, requestDigest, outcomeCode string, now time.Time) (Line, OperationReceipt, error) {
	line := cloneLine(input)
	if err := line.normalizeAndValidate(); err != nil || !validOperationID(operationID) || !sha256Digest(requestDigest) || now.IsZero() {
		return Line{}, OperationReceipt{}, errors.New("invalid reconcile operation identity")
	}
	var updated OperationReceipt
	err := store.db.Update(func(transaction *bolt.Tx) error {
		operations, lines := transaction.Bucket(operationBucket), transaction.Bucket(linesBucket)
		cards := transaction.Bucket(cardsBucket)
		metadata := transaction.Bucket(metadataBucket)
		payload := operations.Get([]byte(operationID))
		if payload == nil {
			return ErrOperationNotFound
		}
		var current OperationReceipt
		if err := json.Unmarshal(payload, &current); err != nil || current.Validate() != nil {
			return errors.New("stored operation receipt is corrupt")
		}
		if current.RequestDigest != requestDigest {
			return ErrOperationReused
		}
		if current.State != OperationUnknown || current.LineID != line.ID || current.CardID != line.CardID ||
			(current.Kind != OperationProvision && current.Kind != OperationReprovision) || current.EnableAfterSuccess == nil {
			return ErrOperationStateChanged
		}
		linePayload := lines.Get([]byte(current.LineID))
		if linePayload == nil {
			return ErrNotFound
		}
		var prior Line
		if err := json.Unmarshal(linePayload, &prior); err != nil || prior.normalizeAndValidate() != nil {
			return errors.New("stored line is corrupt")
		}
		line.Enabled = *current.EnableAfterSuccess
		if current.Kind == OperationProvision {
			line.HardwareProvisionState = "provisioned"
		}
		if prior.CardID != line.CardID {
			if owner := cards.Get([]byte(line.CardID)); owner != nil && string(owner) != line.ID {
				return ErrCardInUse
			}
			if err := cards.Delete([]byte(prior.CardID)); err != nil {
				return err
			}
			if err := cards.Put([]byte(line.CardID), []byte(line.ID)); err != nil {
				return err
			}
		}
		linePayload, err := json.Marshal(line)
		if err != nil {
			return err
		}
		if err := lines.Put([]byte(line.ID), linePayload); err != nil {
			return err
		}
		revision := bytesUint64(metadata.Get(revisionKey)) + 1
		current.State = OperationReconciled
		current.OutcomeCode = outcomeCode
		current.ErrorCode = ""
		current.ErrorDetail = ""
		current.CommittedCatalogRevision = revision
		current.UpdatedAt = now.UTC()
		next, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := operations.Put([]byte(operationID), next); err != nil {
			return err
		}
		if err := metadata.Put(revisionKey, uint64Bytes(revision)); err != nil {
			return err
		}
		updated = current
		return nil
	})
	return cloneLine(line), updated, err
}

func validOperationID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-_.:", char) {
			continue
		}
		return false
	}
	return true
}

// ValidOperationID exposes the durable identifier grammar to API adapters
// without exposing receipt persistence internals.
func ValidOperationID(value string) bool { return validOperationID(value) }

func sha256Digest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
