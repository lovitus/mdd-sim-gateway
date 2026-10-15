package agentlink

import (
	"context"
	"errors"
	"strings"
)

type ReaderReadbackScope string

// Empty scope retains the original full SIM and secure-element readback.
const ReaderReadbackSIMIdentity ReaderReadbackScope = "sim_identity"

type ReaderReadbackRequest struct {
	Scope                ReaderReadbackScope `json:"scope,omitempty"`
	OperationID          string              `json:"operation_id"`
	ProcessGeneration    string              `json:"process_generation"`
	ReaderName           string              `json:"reader_name"`
	CardID               string              `json:"card_id"`
	SIMSessionGeneration string              `json:"sim_session_generation"`
}

type ReaderReadbackResponse struct {
	Scope                ReaderReadbackScope `json:"scope,omitempty"`
	OperationID          string              `json:"operation_id"`
	ProcessGeneration    string              `json:"process_generation"`
	ReaderName           string              `json:"reader_name"`
	CardID               string              `json:"card_id"`
	SIMSessionGeneration string              `json:"sim_session_generation"`
	State                string              `json:"state"`
	ErrorCode            string              `json:"error_code,omitempty"`
	Reader               *ReaderFact         `json:"reader,omitempty"`
	SecureElements       []EUICCSlotFact     `json:"secure_elements,omitempty"`
}

func (request ReaderReadbackRequest) Validate() error {
	if request.Scope != "" && request.Scope != ReaderReadbackSIMIdentity {
		return errors.New("invalid reader readback scope")
	}
	if !validIdentifier(request.OperationID) || !validIdentifier(request.ProcessGeneration) ||
		!validReaderName(request.ReaderName) || !validCardID(request.CardID) ||
		!validIdentifier(request.SIMSessionGeneration) {
		return errors.New("invalid reader readback identity")
	}
	return nil
}

func (response ReaderReadbackResponse) ValidateFor(request ReaderReadbackRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if response.Scope != request.Scope || response.OperationID != request.OperationID ||
		response.ProcessGeneration != request.ProcessGeneration ||
		response.ReaderName != request.ReaderName ||
		response.CardID != request.CardID ||
		response.SIMSessionGeneration != request.SIMSessionGeneration {
		return errors.New("reader readback response identity mismatch")
	}
	switch response.State {
	case "applied":
		if response.Reader == nil || response.ErrorCode != "" {
			return errors.New("successful reader readback lacks reader facts")
		}
		if request.Scope == ReaderReadbackSIMIdentity {
			if response.Reader.ReaderName != request.ReaderName || response.Reader.CardID != request.CardID ||
				response.Reader.SessionGeneration != request.SIMSessionGeneration {
				return errors.New("SIM identity readback contains mismatched reader facts")
			}
			if response.Reader.EUICC != nil || len(response.Reader.SecureElements) != 0 || len(response.SecureElements) != 0 {
				return errors.New("SIM identity readback must not assert secure-element observations")
			}
		}
		if err := (TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{*response.Reader}}).Validate(); err != nil {
			return errors.New("successful reader readback contains invalid reader facts")
		}
	case "unknown", "failed":
		if strings.TrimSpace(response.ErrorCode) == "" {
			return errors.New("unsuccessful reader readback lacks error code")
		}
	default:
		return errors.New("invalid reader readback state")
	}
	return nil
}

type ReaderReadbackExecutor interface {
	ReadReader(context.Context, ReaderReadbackRequest) ReaderReadbackResponse
}

// Only executors implementing the scoped transaction may advertise this feature.
type ReaderSIMIdentityExecutor interface {
	ReaderReadbackExecutor
	SupportsReaderSIMIdentity() bool
}
