// Package events defines the direct, versioned state event contract shared by
// the Go core, agents, and VoWiFi runtimes.
package events

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

const SchemaVersion = 1

type ProducerRole string

const (
	RoleCore   ProducerRole = "mdd-core"
	RoleAgent  ProducerRole = "mdd-agent"
	RoleVoWiFi ProducerRole = "mdd-vowifi"
)

var (
	ErrInvalidEvent         = errors.New("invalid state event")
	ErrUnauthorizedProducer = errors.New("state event producer is not the current binding")
	ErrGenerationReused     = errors.New("a replaced producer generation cannot become current again")
)

var owners = map[state.Layer]ProducerRole{
	state.LayerIntent:        RoleCore,
	state.LayerAgentLink:     RoleAgent,
	state.LayerHardware:      RoleAgent,
	state.LayerCard:          RoleAgent,
	state.LayerCardRoute:     RoleCore,
	state.LayerPIN:           RoleAgent,
	state.LayerCellularData:  RoleAgent,
	state.LayerCellularVoice: RoleAgent,
	state.LayerCellularSMS:   RoleAgent,
	state.LayerEngineProcess: RoleCore,
	state.LayerTunnel:        RoleVoWiFi,
	state.LayerIMS:           RoleVoWiFi,
	state.LayerAdmission:     RoleCore,
	state.LayerMessaging:     RoleVoWiFi,
	state.LayerMedia:         RoleVoWiFi,
	state.LayerCall:          RoleCore,
}

type Event struct {
	SchemaVersion int             `json:"schema_version"`
	EventID       string          `json:"event_id"`
	LineID        string          `json:"line_id"`
	ProducerRole  ProducerRole    `json:"producer_role"`
	ProducerID    string          `json:"producer_id"`
	Layer         state.Layer     `json:"layer"`
	Condition     state.Condition `json:"condition"`
	Available     bool            `json:"available"`
	Code          string          `json:"code,omitempty"`
	Generation    string          `json:"generation"`
	Sequence      uint64          `json:"sequence"`
	ObservedAt    time.Time       `json:"observed_at"`
}

// Record is the durable server receipt. ReceivedAt is assigned at ingestion;
// Epoch is assigned by the core when it accepts a new producer generation.
// An Agent's wall clock or locally reset counter never decides freshness.
type Record struct {
	ReceivedAt time.Time `json:"received_at"`
	Epoch      uint64    `json:"epoch"`
	Event      Event     `json:"event"`
}

func Definitions(ttl time.Duration) (map[state.Layer]state.Definition, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("%w: ttl must be positive", ErrInvalidEvent)
	}
	definitions := make(map[state.Layer]state.Definition, len(owners))
	for layer, owner := range owners {
		definitions[layer] = state.Definition{Owner: string(owner), TTL: ttl}
	}
	return definitions, nil
}

func (record Record) Observation() (string, state.Observation, error) {
	event := record.Event
	if err := validateEvent(event); err != nil || record.ReceivedAt.IsZero() || record.Epoch == 0 {
		if err != nil {
			return "", state.Observation{}, err
		}
		return "", state.Observation{}, ErrInvalidEvent
	}
	return strings.TrimSpace(event.LineID), state.Observation{
		Layer: event.Layer, Condition: event.Condition, Available: event.Available,
		Code: strings.TrimSpace(event.Code), Source: string(event.ProducerRole),
		ProducerID: strings.TrimSpace(event.ProducerID),
		Generation: strings.TrimSpace(event.Generation), Epoch: record.Epoch,
		Sequence: event.Sequence, ObservedAt: event.ObservedAt,
		ReceivedAt: record.ReceivedAt,
	}, nil
}

func validateEvent(event Event) error {
	if event.SchemaVersion != SchemaVersion || strings.TrimSpace(event.EventID) == "" ||
		strings.TrimSpace(event.LineID) == "" || strings.TrimSpace(event.ProducerID) == "" ||
		strings.TrimSpace(event.Generation) == "" || event.Sequence == 0 || event.ObservedAt.IsZero() {
		return ErrInvalidEvent
	}
	if !state.ValidCondition(event.Condition) ||
		(event.Code != "" && !state.ValidCode(strings.TrimSpace(event.Code))) {
		return ErrInvalidEvent
	}
	if event.ProducerRole != RoleCore && event.ProducerRole != RoleAgent && event.ProducerRole != RoleVoWiFi {
		return fmt.Errorf("%w: producer role %q", ErrInvalidEvent, event.ProducerRole)
	}
	owner, exists := owners[event.Layer]
	if !exists {
		return fmt.Errorf("%w: layer %q", ErrInvalidEvent, event.Layer)
	}
	if owner != event.ProducerRole {
		return fmt.Errorf("%w: layer %s is owned by %s", state.ErrWrongOwner, event.Layer, owner)
	}
	return nil
}
