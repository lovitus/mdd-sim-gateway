package agentmodem

import (
	"context"
	"errors"
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
)

type Operation struct {
	AttachmentID string
	EquipmentID  string
	CardID       string
	Action       OperationAction
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
	Call CallResult
}

type Operator interface {
	Operate(context.Context, Operation) (OperationResult, error)
}

func ValidateOperationTarget(facts []Fact, operation Operation) error {
	if operation.Action != OperationCallStatus && operation.Action != OperationCallHangup {
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
