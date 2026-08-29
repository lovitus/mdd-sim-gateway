package agentmodem

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrOperationTargetReplaced = errors.New("modem operation target was replaced")
	ErrOperationUnavailable    = errors.New("modem operation is unavailable")
)

type OperationAction string

const (
	OperationCallStatus OperationAction = "call_status"
	OperationCallHangup OperationAction = "call_hangup"
	OperationCallDial   OperationAction = "call_dial"
	OperationCallAnswer OperationAction = "call_answer"
	OperationCallRenew  OperationAction = "call_renew"
)

type Operation struct {
	OperationID  string
	AttachmentID string
	EquipmentID  string
	CardID       string
	Action       OperationAction
	LeaseID      string
	Number       string
}

type CallResult struct {
	State             string
	Direction         string
	Number            string
	ObservedAt        time.Time
	Authoritative     bool
	TerminalConfirmed bool
	Strategy          string
}

type OperationResult struct {
	Call       CallResult
	LeaseID    string
	LeaseUntil time.Time
}

type Operator interface {
	Operate(context.Context, Operation) (OperationResult, error)
}

type ManagedOperator interface {
	Operator
	Run(context.Context) error
}

type MediaTarget struct {
	AttachmentID string
	EquipmentID  string
	CardID       string
}

// MediaOperator opens the fixed 8 kHz, signed-16-bit, mono voice PCM stream
// for one exact live attachment. Closing the returned endpoint must disable
// the modem PCM mode as well as release the serial function.
type MediaOperator interface {
	OpenVoicePCM(context.Context, MediaTarget) (io.ReadWriteCloser, error)
}

func ValidateMediaTarget(facts []Fact, target MediaTarget) error {
	return ValidateOperationTarget(facts, Operation{
		AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID,
		CardID: target.CardID, Action: OperationCallStatus,
	})
}

func ValidateOperationTarget(facts []Fact, operation Operation) error {
	if operation.Action != OperationCallStatus && operation.Action != OperationCallHangup &&
		operation.Action != OperationCallDial && operation.Action != OperationCallAnswer {
		return errors.New("unsupported modem operation")
	}
	matches := 0
	var target Fact
	for _, fact := range facts {
		if fact.AttachmentID == operation.AttachmentID && fact.EquipmentID == operation.EquipmentID &&
			fact.SIM.ICCID == operation.CardID {
			matches++
			target = fact
		}
	}
	if matches != 1 {
		return ErrOperationTargetReplaced
	}
	if target.AT.State != ATControlReady || !target.AT.CallSignalling {
		return ErrOperationUnavailable
	}
	return nil
}
