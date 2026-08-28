// Package agentlink defines the narrow, authenticated WSS control channel
// between one MDD Agent and Core. It deliberately exposes a high-level AKA
// operation instead of a general APDU tunnel.
package agentlink

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const SchemaVersion = 1

type AKAApplication string

const (
	AKAApplicationUSIM AKAApplication = "usim"
	AKAApplicationISIM AKAApplication = "isim"
)

type Hello struct {
	SchemaVersion     int    `json:"schema_version"`
	AgentID           string `json:"agent_id"`
	ProcessGeneration string `json:"process_generation"`
}

// AKARequest targets one exact live card attachment. SessionGeneration is
// replaced on removal/reinsertion even when reader name and ATR are unchanged.
// CardID is the durable EID/ICCID selected by Core and must match the session's
// discovered identity; it is never inferred from reader order.
type AKARequest struct {
	OperationID       string         `json:"operation_id"`
	SessionGeneration string         `json:"session_generation"`
	CardID            string         `json:"card_id"`
	Application       AKAApplication `json:"application"`
	RAND              []byte         `json:"rand"`
	AUTN              []byte         `json:"autn"`
}

// AKAResponse contains only the response body and status from the one
// AUTHENTICATE operation. Parsing RES/CK/IK/AUTS remains in the isolated
// VoWiFi provider; Core must not persist or log Body.
type AKAResponse struct {
	OperationID       string       `json:"operation_id"`
	SessionGeneration string       `json:"session_generation"`
	Body              []byte       `json:"body,omitempty"`
	SW1               byte         `json:"sw1,omitempty"`
	SW2               byte         `json:"sw2,omitempty"`
	Failure           *RemoteError `json:"failure,omitempty"`
}

type RemoteError struct {
	Kind       string `json:"kind"`
	Code       string `json:"code"`
	Retryable  bool   `json:"retryable"`
	RetryAfter int64  `json:"retry_after_ms,omitempty"`
}

func (failure *RemoteError) Error() string {
	if failure == nil {
		return "Agent operation failed"
	}
	return fmt.Sprintf("Agent operation failed (%s/%s)", failure.Kind, failure.Code)
}

type Authenticator interface {
	AuthenticateAKA(context.Context, AKARequest) AKAResponse
}

func (hello Hello) Validate() error {
	if hello.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported Agent link schema version %d", hello.SchemaVersion)
	}
	if !validIdentifier(hello.AgentID) || !validIdentifier(hello.ProcessGeneration) {
		return errors.New("invalid Agent link identity or process generation")
	}
	return nil
}

func (request AKARequest) Validate() error {
	if !validIdentifier(request.OperationID) || !validIdentifier(request.SessionGeneration) ||
		!validCardID(request.CardID) {
		return errors.New("invalid AKA operation, session generation, or card identity")
	}
	if request.Application != AKAApplicationUSIM && request.Application != AKAApplicationISIM {
		return fmt.Errorf("unsupported AKA application %q", request.Application)
	}
	if len(request.RAND) != 16 || len(request.AUTN) != 16 {
		return errors.New("AKA RAND and AUTN must each be 16 bytes")
	}
	return nil
}

func (response AKAResponse) ValidateFor(request AKARequest) error {
	if response.OperationID != request.OperationID || response.SessionGeneration != request.SessionGeneration {
		return errors.New("AKA response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || len(response.Body) != 0 || response.SW1 != 0 || response.SW2 != 0 {
			return errors.New("invalid failed AKA response")
		}
		return nil
	}
	if len(response.Body) > 1024 || response.SW1 == 0 && response.SW2 == 0 {
		return errors.New("invalid successful AKA response")
	}
	return nil
}

func (failure RemoteError) Validate() error {
	switch failure.Kind {
	case "not_ready", "conflict", "rejected", "transport", "failed":
	default:
		return errors.New("invalid remote error kind")
	}
	if !validIdentifier(failure.Code) || failure.RetryAfter < 0 || failure.RetryAfter > 3_600_000 {
		return errors.New("invalid remote error code or retry delay")
	}
	return nil
}

func validIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			continue
		}
		return false
	}
	return true
}

func validCardID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
