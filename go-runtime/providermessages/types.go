// Package providermessages carries durable SMS business events from a local
// VoWiFi provider into Core. It is deliberately separate from provider facts:
// registration/readiness snapshots are replaceable state, while messages and
// delivery reports are append-only business records.
package providermessages

import (
	"errors"
	"strings"
	"time"
)

const SchemaVersion = 1

type Kind string

const (
	KindReceived  Kind = "received"
	KindSubmitted Kind = "submitted"
	KindDelivery  Kind = "delivery"
)

type Event struct {
	SchemaVersion     int       `json:"schema_version"`
	EventID           string    `json:"event_id"`
	LineID            string    `json:"line_id"`
	ProviderID        string    `json:"provider_id"`
	ProcessGeneration string    `json:"process_generation"`
	Kind              Kind      `json:"kind"`
	ObservedAt        time.Time `json:"observed_at"`
	MessageID         string    `json:"message_id,omitempty"`
	Part              int       `json:"part,omitempty"`
	Sender            string    `json:"sender,omitempty"`
	Recipient         string    `json:"recipient,omitempty"`
	Body              string    `json:"body,omitempty"`
	CallID            string    `json:"call_id,omitempty"`
	InReplyTo         string    `json:"in_reply_to,omitempty"`
	RPMR              int       `json:"rp_mr,omitempty"`
	State             string    `json:"state,omitempty"`
	SIPCode           int       `json:"sip_code,omitempty"`
	RPCause           int       `json:"rp_cause,omitempty"`
	Error             string    `json:"error,omitempty"`
}

type Record struct {
	Event
	ReceivedAt time.Time `json:"received_at"`
}

func (event Event) Validate() error {
	if event.SchemaVersion != SchemaVersion || !identifier(event.EventID) ||
		!identifier(event.LineID) || !identifier(event.ProviderID) ||
		!identifier(event.ProcessGeneration) || event.ObservedAt.IsZero() {
		return errors.New("invalid provider message identity")
	}
	if len(event.Body) > 16<<10 || len(event.Error) > 4096 || event.Part < 0 ||
		event.SIPCode < 0 || event.SIPCode > 699 || event.RPMR < 0 || event.RPMR > 255 {
		return errors.New("invalid provider message fields")
	}
	switch event.Kind {
	case KindReceived:
		if strings.TrimSpace(event.Sender) == "" || (event.Body == "" && strings.TrimSpace(event.MessageID) == "") {
			return errors.New("invalid received SMS")
		}
	case KindSubmitted:
		if !identifier(event.MessageID) || event.Part < 1 || strings.TrimSpace(event.Recipient) == "" ||
			(strings.TrimSpace(event.CallID) == "" && event.RPMR == 0) {
			return errors.New("invalid submitted SMS part")
		}
	case KindDelivery:
		if strings.TrimSpace(event.CallID) == "" && strings.TrimSpace(event.InReplyTo) == "" && event.RPMR == 0 {
			return errors.New("delivery report has no correlation identity")
		}
		if strings.TrimSpace(event.State) == "" {
			return errors.New("delivery report has no state")
		}
	default:
		return errors.New("invalid provider message kind")
	}
	return nil
}

func identifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 200 {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
