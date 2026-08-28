// Package agentlink defines the narrow, authenticated WSS control channel
// between one MDD Agent and Core. It deliberately exposes a high-level AKA
// operation instead of a general APDU tunnel.
package agentlink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

type CardIdentityState string

type ReaderCondition string

const (
	CardAbsent              CardIdentityState = "absent"
	CardIdentityDiscovering CardIdentityState = "discovering"
	CardIdentified          CardIdentityState = "identified"
	CardIdentityUnavailable CardIdentityState = "identity_unavailable"
)

const (
	ReaderStarting   ReaderCondition = "starting"
	ReaderReady      ReaderCondition = "ready"
	ReaderRecovering ReaderCondition = "recovering"
)

// ReaderFact describes one current PC/SC attachment. ReaderName is only a
// local attachment label. SessionGeneration fences one insertion, while
// CardID is the durable ICCID when the card exposes one.
type ReaderFact struct {
	ReaderName        string            `json:"reader_name"`
	CardPresent       bool              `json:"card_present"`
	SessionGeneration string            `json:"session_generation,omitempty"`
	CardID            string            `json:"card_id,omitempty"`
	IdentityState     CardIdentityState `json:"identity_state"`
	ATRSHA256         string            `json:"atr_sha256,omitempty"`
}

type TopologySnapshot struct {
	ReaderCondition ReaderCondition `json:"reader_condition"`
	ReaderDetail    string          `json:"reader_detail,omitempty"`
	Readers         []ReaderFact    `json:"readers"`
}

// HealthReport is sent every ten seconds in production. Topology is present
// on the first report and whenever TopologyRevision changes; an unchanged
// report renews only the application heartbeat.
type HealthReport struct {
	SchemaVersion    int               `json:"schema_version"`
	Sequence         uint64            `json:"sequence"`
	TopologyRevision string            `json:"topology_revision"`
	Topology         *TopologySnapshot `json:"topology,omitempty"`
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

func (topology TopologySnapshot) Validate() error {
	if topology.ReaderCondition != ReaderStarting && topology.ReaderCondition != ReaderReady &&
		topology.ReaderCondition != ReaderRecovering {
		return errors.New("Agent topology has an invalid reader condition")
	}
	if len(topology.ReaderDetail) > 1024 || topology.ReaderCondition != ReaderRecovering && topology.ReaderDetail != "" ||
		topology.ReaderCondition != ReaderReady && len(topology.Readers) != 0 {
		return errors.New("Agent topology reader condition has inconsistent detail or attachments")
	}
	if len(topology.Readers) > 64 {
		return errors.New("Agent topology has too many reader attachments")
	}
	previous := ""
	for index, reader := range topology.Readers {
		if !validReaderName(reader.ReaderName) || index > 0 && reader.ReaderName <= previous {
			return errors.New("Agent topology reader names must be valid, unique, and sorted")
		}
		previous = reader.ReaderName
		if reader.SessionGeneration != "" && !validIdentifier(reader.SessionGeneration) ||
			reader.CardID != "" && !validCardID(reader.CardID) ||
			reader.ATRSHA256 != "" && !validSHA256(reader.ATRSHA256) {
			return errors.New("Agent topology contains an invalid card fact")
		}
		switch reader.IdentityState {
		case CardAbsent:
			if reader.CardPresent || reader.SessionGeneration != "" || reader.CardID != "" || reader.ATRSHA256 != "" {
				return errors.New("absent topology attachment contains card state")
			}
		case CardIdentityDiscovering, CardIdentityUnavailable:
			if !reader.CardPresent || reader.SessionGeneration == "" || reader.CardID != "" {
				return errors.New("unidentified topology card has inconsistent state")
			}
		case CardIdentified:
			if !reader.CardPresent || reader.SessionGeneration == "" || reader.CardID == "" {
				return errors.New("identified topology card has inconsistent state")
			}
		default:
			return errors.New("Agent topology has an unknown identity state")
		}
	}
	return nil
}

func (topology TopologySnapshot) Revision() (string, error) {
	if err := topology.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(topology)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (report HealthReport) Validate() error {
	if report.SchemaVersion != SchemaVersion || report.Sequence == 0 || !validSHA256(report.TopologyRevision) {
		return errors.New("invalid Agent health identity, sequence, or topology revision")
	}
	if report.Topology != nil {
		revision, err := report.Topology.Revision()
		if err != nil || revision != report.TopologyRevision {
			return errors.New("Agent health topology does not match its revision")
		}
	}
	return nil
}

func NormalizeTopology(topology TopologySnapshot) TopologySnapshot {
	result := TopologySnapshot{
		ReaderCondition: topology.ReaderCondition, ReaderDetail: topology.ReaderDetail,
		Readers: make([]ReaderFact, len(topology.Readers)),
	}
	copy(result.Readers, topology.Readers)
	sort.Slice(result.Readers, func(left, right int) bool {
		return result.Readers[left].ReaderName < result.Readers[right].ReaderName
	})
	return result
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

func validReaderName(value string) bool {
	if len(value) < 1 || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
