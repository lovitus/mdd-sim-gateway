package agentlink

import (
	"context"
	"errors"
)

const ModemSoftRestart = "soft_restart"

type ModemRecoveryCommand struct {
	OperationID string `json:"operation_id"`
	EquipmentID string `json:"equipment_id"`
	CardID      string `json:"card_id"`
	Action      string `json:"action"`
}

type ModemRecoveryRequest struct {
	ModemRecoveryCommand
	ProcessGeneration    string `json:"process_generation"`
	AttachmentID         string `json:"attachment_id"`
	SIMSessionGeneration string `json:"sim_session_generation"`
}

type ModemRecoveryResponse struct {
	OperationID          string       `json:"operation_id"`
	EquipmentID          string       `json:"equipment_id"`
	CardID               string       `json:"card_id"`
	AttachmentID         string       `json:"attachment_id"`
	SIMSessionGeneration string       `json:"sim_session_generation"`
	Action               string       `json:"action"`
	State                string       `json:"state"`
	Failure              *RemoteError `json:"failure,omitempty"`
}

type ModemRecoveryExecutor interface {
	ExecuteModemRecovery(context.Context, ModemRecoveryRequest) ModemRecoveryResponse
}

func (command ModemRecoveryCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEquipmentID(command.EquipmentID) ||
		!validCardID(command.CardID) || command.Action != ModemSoftRestart {
		return errors.New("invalid modem recovery command")
	}
	return nil
}

func (request ModemRecoveryRequest) Validate() error {
	if request.ModemRecoveryCommand.Validate() != nil || !validIdentifier(request.ProcessGeneration) ||
		!validIdentifier(request.AttachmentID) || !validIdentifier(request.SIMSessionGeneration) {
		return errors.New("invalid modem recovery request fence")
	}
	return nil
}

func (response ModemRecoveryResponse) ValidateFor(request ModemRecoveryRequest) error {
	if response.OperationID != request.OperationID || response.EquipmentID != request.EquipmentID ||
		response.CardID != request.CardID || response.AttachmentID != request.AttachmentID ||
		response.SIMSessionGeneration != request.SIMSessionGeneration || response.Action != request.Action {
		return errors.New("modem recovery response identity mismatch")
	}
	if response.State == "accepted" {
		if response.Failure != nil {
			return errors.New("accepted modem recovery has a failure")
		}
		return nil
	}
	if response.State != "unavailable" && response.State != "failed" || response.Failure == nil || response.Failure.Validate() != nil {
		return errors.New("invalid modem recovery outcome")
	}
	return nil
}
