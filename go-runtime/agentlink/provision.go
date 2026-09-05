package agentlink

import (
	"errors"
	"strings"
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
