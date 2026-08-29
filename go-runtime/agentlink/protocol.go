package agentlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/coder/websocket"
)

const (
	kindHello         = "hello"
	kindHelloAck      = "hello_ack"
	kindAKARequest    = "aka_request"
	kindAKAResponse   = "aka_response"
	kindModemRequest  = "modem_request"
	kindModemResponse = "modem_response"
	kindHealth        = "health"
)

type envelope struct {
	Kind         string         `json:"kind"`
	RequestID    string         `json:"request_id,omitempty"`
	Hello        *Hello         `json:"hello,omitempty"`
	AKARequest   *AKARequest    `json:"aka_request,omitempty"`
	AKAResult    *AKAResponse   `json:"aka_response,omitempty"`
	ModemRequest *ModemRequest  `json:"modem_request,omitempty"`
	ModemResult  *ModemResponse `json:"modem_response,omitempty"`
	Health       *HealthReport  `json:"health,omitempty"`
}

func readEnvelope(ctx context.Context, socket *websocket.Conn) (envelope, error) {
	messageType, payload, err := socket.Read(ctx)
	if err != nil {
		return envelope{}, err
	}
	if messageType != websocket.MessageText {
		return envelope{}, errors.New("Agent link message is not JSON text")
	}
	return decodeEnvelope(payload)
}

func decodeEnvelope(payload []byte) (envelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var message envelope
	if err := decoder.Decode(&message); err != nil {
		return envelope{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return envelope{}, errors.New("Agent link message has trailing JSON")
	}
	return message, nil
}

func writeEnvelope(ctx context.Context, socket *websocket.Conn, message envelope) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return socket.Write(ctx, websocket.MessageText, payload)
}

func (message envelope) validate() error {
	switch message.Kind {
	case kindHello:
		if message.RequestID != "" || message.Hello == nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.Health != nil {
			return errors.New("invalid Agent hello envelope")
		}
		return message.Hello.Validate()
	case kindHelloAck:
		if message.RequestID != "" || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.Health != nil {
			return errors.New("invalid Agent hello acknowledgement envelope")
		}
		return nil
	case kindAKARequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest == nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.Health != nil {
			return errors.New("invalid AKA request envelope")
		}
		return message.AKARequest.Validate()
	case kindAKAResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult == nil || message.ModemRequest != nil || message.ModemResult != nil || message.Health != nil {
			return errors.New("invalid AKA response envelope")
		}
		return nil
	case kindModemRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest == nil || message.ModemResult != nil || message.Health != nil {
			return errors.New("invalid modem request envelope")
		}
		return message.ModemRequest.Validate()
	case kindModemResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult == nil || message.Health != nil {
			return errors.New("invalid modem response envelope")
		}
		return nil
	case kindHealth:
		if message.RequestID != "" || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.Health == nil {
			return errors.New("invalid Agent health envelope")
		}
		return message.Health.Validate()
	default:
		return errors.New("unknown Agent link message kind")
	}
}
