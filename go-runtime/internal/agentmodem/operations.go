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
	OperationSMSList    OperationAction = "sms_list"
	OperationSMSSend    OperationAction = "sms_send"
)

type Operation struct {
	OperationID  string
	AttachmentID string
	EquipmentID  string
	CardID       string
	Action       OperationAction
	LeaseID      string
	Number       string
	Body         string
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
	SMS        SMSResult
}

type SMSMessage struct {
	Index       int
	State       string
	Direction   string
	Peer        string
	Body        string
	ObservedAt  time.Time
	Fingerprint string
	Reference   int
	Delivery    string
}

type SMSResult struct {
	State      string
	Messages   []SMSMessage
	References []int
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
		operation.Action != OperationCallDial && operation.Action != OperationCallAnswer &&
		operation.Action != OperationSMSList && operation.Action != OperationSMSSend {
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
	if target.AT.State != ATControlReady ||
		(operation.Action == OperationSMSList || operation.Action == OperationSMSSend) && !target.AT.SMS ||
		operation.Action != OperationSMSList && operation.Action != OperationSMSSend && !target.AT.CallSignalling {
		return ErrOperationUnavailable
	}
	return nil
}
