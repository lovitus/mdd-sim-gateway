package linecatalog

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// OperationSchemaVersion is the durable receipt schema used by future
// provision/reprovision transactions. Receipts deliberately contain no
// subscriber secrets; they only carry identity fences and sanitized outcomes.
const OperationSchemaVersion = 1

type OperationKind string

const (
	OperationClaim       OperationKind = "claim"
	OperationProvision   OperationKind = "provision"
	OperationReprovision OperationKind = "reprovision"
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
	CandidateID              string         `json:"candidate_id,omitempty"`
	CandidateDigest          string         `json:"candidate_digest,omitempty"`
	ExpectedCatalogRevision  uint64         `json:"expected_catalog_revision"`
	CommittedCatalogRevision uint64         `json:"committed_catalog_revision,omitempty"`
	LineID                   string         `json:"line_id,omitempty"`
	CardID                   string         `json:"card_id,omitempty"`
	AgentID                  string         `json:"agent_id,omitempty"`
	ProcessGeneration        string         `json:"process_generation,omitempty"`
	AttachmentID             string         `json:"attachment_id,omitempty"`
	EquipmentID              string         `json:"equipment_id,omitempty"`
	SIMSessionGeneration     string         `json:"sim_session_generation,omitempty"`
	Step                     string         `json:"step,omitempty"`
	OutcomeCode              string         `json:"outcome_code,omitempty"`
	ErrorCode                string         `json:"error_code,omitempty"`
	ErrorDetail              string         `json:"error_detail,omitempty"`
	AttemptCount             uint32         `json:"attempt_count"`
	ProviderGeneration       string         `json:"provider_generation,omitempty"`
	RuntimeGeneration        string         `json:"runtime_generation,omitempty"`
}

func (receipt OperationReceipt) Validate() error {
	if receipt.SchemaVersion != OperationSchemaVersion {
		return errors.New("unsupported operation receipt schema")
	}
	if !validOperationID(receipt.OperationID) {
		return errors.New("invalid operation id")
	}
	if receipt.Kind != OperationClaim && receipt.Kind != OperationProvision && receipt.Kind != OperationReprovision {
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
	if receipt.CandidateDigest != "" && !sha256Digest(receipt.CandidateDigest) {
		return errors.New("invalid candidate digest")
	}
	if receipt.AttemptCount == 0 {
		return errors.New("operation attempt count is zero")
	}
	if len(receipt.ErrorCode) > 128 || len(receipt.ErrorDetail) > 1024 || len(receipt.Step) > 128 || len(receipt.OutcomeCode) > 128 {
		return errors.New("operation diagnostic is too large")
	}
	return nil
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

func sha256Digest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
