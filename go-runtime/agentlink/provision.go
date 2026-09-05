package agentlink

import (
	"context"
	"errors"
	"strings"
)

type ProvisionState string

const (
	ProvisionPrepared ProvisionState = "prepared"
	ProvisionApplied  ProvisionState = "applied"
	ProvisionFailed   ProvisionState = "failed"
	ProvisionUnknown  ProvisionState = "unknown"
)

// ProvisionCommand is the Core-side intent for creating or replacing one line.
// It is deliberately separate from ModemCommand until an Agent implementation
// can execute the complete hardware transaction.
type ProvisionCommand struct {
	OperationID          string `json:"operation_id"`
	EquipmentID          string `json:"equipment_id"`
	CardID               string `json:"card_id"`
	AttachmentID         string `json:"attachment_id"`
	SIMSessionGeneration string `json:"sim_session_generation"`
	IMSI                 string `json:"imsi"`
	MCC                  string `json:"mcc"`
	MNC                  string `json:"mnc"`
	IMEI                 string `json:"imei"`
	IMEISV               string `json:"imeisv,omitempty"`
	MSISDN               string `json:"msisdn,omitempty"`
	SMSC                 string `json:"smsc"`
	ReaderPort           string `json:"reader_port,omitempty"`
	APN                  string `json:"apn,omitempty"`
	IDRMode              string `json:"idr_mode,omitempty"`
	CPMode               string `json:"cp_mode,omitempty"`
}

// ProvisionRequest is the Agent-side request after Core resolves the current
// attachment. The Agent must re-check all identity fields before touching
// hardware and must never report Applied before the full transaction commits.
type ProvisionRequest struct {
	ProvisionCommand
}

type ProvisionResponse struct {
	OperationID          string         `json:"operation_id"`
	State                ProvisionState `json:"state"`
	EquipmentID          string         `json:"equipment_id"`
	CardID               string         `json:"card_id"`
	SIMSessionGeneration string         `json:"sim_session_generation"`
	Step                 string         `json:"step,omitempty"`
	ErrorCode            string         `json:"error_code,omitempty"`
	Error                string         `json:"error,omitempty"`
}

type ProvisionExecutor interface {
	ExecuteProvision(context.Context, ProvisionRequest) ProvisionResponse
}

func (response ProvisionResponse) Validate() error {
	if !validIdentifier(response.OperationID) ||
		!validEquipmentID(response.EquipmentID) ||
		!validCardID(response.CardID) ||
		strings.TrimSpace(response.SIMSessionGeneration) == "" {
		return errors.New("invalid provision response identity")
	}
	switch response.State {
	case ProvisionPrepared, ProvisionApplied, ProvisionFailed, ProvisionUnknown:
	default:
		return errors.New("invalid provision response state")
	}
	if response.State == ProvisionFailed && strings.TrimSpace(response.ErrorCode) == "" {
		return errors.New("failed provision response requires an error code")
	}
	return nil
}

func (command ProvisionCommand) Validate() error {
	if !validIdentifier(command.OperationID) ||
		!validEquipmentID(command.EquipmentID) ||
		!validCardID(command.CardID) ||
		!validIdentifier(command.AttachmentID) ||
		strings.TrimSpace(command.SIMSessionGeneration) == "" {
		return errors.New("invalid provision operation or attachment identity")
	}
	if len(command.IMSI) < 5 || len(command.IMSI) > 18 || !allDigits(command.IMSI) ||
		len(command.MCC) != 3 || !allDigits(command.MCC) ||
		(len(command.MNC) != 2 && len(command.MNC) != 3) || !allDigits(command.MNC) ||
		len(command.IMEI) != 15 || !allDigits(command.IMEI) {
		return errors.New("invalid provision subscriber or hardware identity")
	}
	if command.IMEISV != "" && (len(command.IMEISV) != 16 || !allDigits(command.IMEISV)) {
		return errors.New("invalid provision imeisv")
	}
	if strings.TrimSpace(command.SMSC) == "" {
		return errors.New("provision requires an SMSC")
	}
	if len(command.APN) > 128 || len(command.IDRMode) > 16 || len(command.CPMode) > 16 ||
		len(command.ReaderPort) > 256 || len(command.MSISDN) > 32 {
		return errors.New("provision field exceeds limit")
	}
	return nil
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
