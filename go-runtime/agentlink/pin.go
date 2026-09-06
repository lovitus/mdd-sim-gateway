package agentlink

import (
	"errors"
	"strings"
)

// SIMPINAction is deliberately separate from ModemAction: PIN credentials
// must never pass through call/SMS payload fields or generic AT tunnels.
type SIMPINAction string

const (
	SIMPINVerify     SIMPINAction = "verify"
	SIMPINChange     SIMPINAction = "change"
	SIMPINSetEnabled SIMPINAction = "set_enabled"
	SIMPINStatus     SIMPINAction = "status"
)

// SIMPINCommand names one stable card and exactly one kind of attachment.
// Core resolves it to a live insertion before forwarding the secret.
type SIMPINCommand struct {
	OperationID          string       `json:"operation_id"`
	CardID               string       `json:"card_id"`
	ReaderName           string       `json:"reader_name,omitempty"`
	EquipmentID          string       `json:"equipment_id,omitempty"`
	Action               SIMPINAction `json:"action"`
	PIN                  string       `json:"pin"`
	NewPIN               string       `json:"new_pin,omitempty"`
	Enabled              *bool        `json:"enabled,omitempty"`
	PreflightOperationID string       `json:"preflight_operation_id,omitempty"`
}

// SIMPINRequest adds the ephemeral Agent process and insertion fences. The
// Agent must recheck every field before sending an APDU or AT command.
type SIMPINRequest struct {
	OperationID          string       `json:"operation_id"`
	ProcessGeneration    string       `json:"process_generation"`
	CardID               string       `json:"card_id"`
	ReaderName           string       `json:"reader_name,omitempty"`
	AttachmentID         string       `json:"attachment_id,omitempty"`
	EquipmentID          string       `json:"equipment_id,omitempty"`
	SIMSessionGeneration string       `json:"sim_session_generation"`
	Action               SIMPINAction `json:"action"`
	PIN                  string       `json:"pin"`
	NewPIN               string       `json:"new_pin,omitempty"`
	Enabled              *bool        `json:"enabled,omitempty"`
}

// SIMPINResponse intentionally contains no credential material.
type SIMPINResponse struct {
	OperationID          string       `json:"operation_id"`
	CardID               string       `json:"card_id"`
	ReaderName           string       `json:"reader_name,omitempty"`
	AttachmentID         string       `json:"attachment_id,omitempty"`
	EquipmentID          string       `json:"equipment_id,omitempty"`
	SIMSessionGeneration string       `json:"sim_session_generation"`
	Action               SIMPINAction `json:"action"`
	State                string       `json:"state"`
	AttemptsRemaining    *uint32      `json:"attempts_remaining,omitempty"`
	Failure              *RemoteError `json:"failure,omitempty"`
}

func (command SIMPINCommand) Validate() error {
	return validateSIMPIN(command.OperationID, "", command.CardID, command.ReaderName, "",
		command.EquipmentID, "", command.Action, command.PIN, command.NewPIN, command.Enabled,
		command.PreflightOperationID)
}

func (request SIMPINRequest) Validate() error {
	return validateSIMPIN(request.OperationID, request.ProcessGeneration, request.CardID,
		request.ReaderName, request.AttachmentID, request.EquipmentID, request.SIMSessionGeneration,
		request.Action, request.PIN, request.NewPIN, request.Enabled, "")
}

func validateSIMPIN(operationID, process, cardID, reader, attachment, equipment, session string,
	action SIMPINAction, pin, newPIN string, enabled *bool, preflightOperationID string) error {
	if !validIdentifier(operationID) || !validCardID(cardID) {
		return errors.New("invalid SIM PIN operation identity")
	}
	reader = strings.TrimSpace(reader)
	equipment = strings.TrimSpace(equipment)
	if (reader == "") == (equipment == "") || reader != "" && !validReaderName(reader) ||
		equipment != "" && !validEquipmentID(equipment) {
		return errors.New("SIM PIN target must be exactly one reader or modem")
	}
	if process != "" && (!validIdentifier(process) || !validIdentifier(session)) {
		return errors.New("invalid SIM PIN insertion fence")
	}
	if equipment != "" && process != "" && !validIdentifier(strings.TrimSpace(attachment)) {
		return errors.New("modem SIM PIN request has no attachment fence")
	}
	if reader != "" && attachment != "" {
		return errors.New("reader SIM PIN request has a modem attachment")
	}
	switch action {
	case SIMPINStatus:
		if pin != "" || newPIN != "" || enabled != nil || preflightOperationID != "" {
			return errors.New("invalid status PIN fields")
		}
	case SIMPINVerify:
		if !pinDigits(pin) || newPIN != "" || enabled != nil || !validIdentifier(preflightOperationID) && process == "" {
			return errors.New("invalid verify PIN fields")
		}
	case SIMPINChange:
		if !pinDigits(pin) || !pinDigits(newPIN) || enabled != nil || !validIdentifier(preflightOperationID) && process == "" {
			return errors.New("invalid change PIN fields")
		}
	case SIMPINSetEnabled:
		if !pinDigits(pin) || newPIN != "" || enabled == nil || !validIdentifier(preflightOperationID) && process == "" {
			return errors.New("invalid PIN enable fields")
		}
	default:
		return errors.New("invalid SIM PIN action")
	}
	return nil
}

func (response SIMPINResponse) Validate() error {
	if !validIdentifier(response.OperationID) || !validCardID(response.CardID) ||
		(response.ReaderName == "") == (response.EquipmentID == "") || !validIdentifier(response.SIMSessionGeneration) {
		return errors.New("invalid SIM PIN response identity")
	}
	if response.ReaderName != "" && (!validReaderName(response.ReaderName) || response.AttachmentID != "") ||
		response.EquipmentID != "" && (!validEquipmentID(response.EquipmentID) || !validIdentifier(response.AttachmentID)) {
		return errors.New("invalid SIM PIN response target")
	}
	if response.Action != SIMPINStatus && response.Action != SIMPINVerify && response.Action != SIMPINChange &&
		response.Action != SIMPINSetEnabled {
		return errors.New("invalid SIM PIN response action")
	}
	if response.State != "verified" && response.State != "pin_required" && response.State != "retry_counter" && response.State != "blocked" &&
		response.State != "failed" && response.State != "unavailable" && response.State != "unknown" {
		return errors.New("invalid SIM PIN response state")
	}
	if (response.State == "failed" || response.State == "unavailable" || response.State == "unknown") != (response.Failure != nil) ||
		response.AttemptsRemaining != nil && *response.AttemptsRemaining > 255 ||
		response.State == "retry_counter" && (response.Action != SIMPINStatus || response.AttemptsRemaining == nil) ||
		response.Action != SIMPINStatus && response.AttemptsRemaining != nil {
		return errors.New("invalid SIM PIN response outcome")
	}
	return nil
}

func pinDigits(value string) bool {
	if len(value) < 4 || len(value) > 8 {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
