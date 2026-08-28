package state

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Layer string

const (
	LayerIntent        Layer = "intent"
	LayerAgentLink     Layer = "agent_link"
	LayerHardware      Layer = "hardware"
	LayerCard          Layer = "card"
	LayerCardRoute     Layer = "card_route"
	LayerPIN           Layer = "pin"
	LayerCellularData  Layer = "cellular_data"
	LayerCellularVoice Layer = "cellular_voice"
	LayerCellularSMS   Layer = "cellular_sms"
	LayerEngineProcess Layer = "engine_process"
	LayerTunnel        Layer = "tunnel"
	LayerIMS           Layer = "ims"
	LayerAdmission     Layer = "admission"
	LayerMessaging     Layer = "messaging"
	LayerMedia         Layer = "media"
	LayerCall          Layer = "call"
)

type Condition string

const (
	ConditionUnknown  Condition = "unknown"
	ConditionInactive Condition = "inactive"
	ConditionStarting Condition = "starting"
	ConditionReady    Condition = "ready"
	ConditionDegraded Condition = "degraded"
	ConditionBackoff  Condition = "backoff"
	ConditionBlocked  Condition = "blocked"
	ConditionFailed   Condition = "failed"
	ConditionActive   Condition = "active"
)

type Definition struct {
	Owner string
	TTL   time.Duration
}

type Observation struct {
	Layer      Layer         `json:"layer"`
	Condition  Condition     `json:"condition"`
	Available  bool          `json:"available"`
	Code       string        `json:"code,omitempty"`
	Detail     string        `json:"detail,omitempty"`
	Source     string        `json:"source"`
	ProducerID string        `json:"producer_id,omitempty"`
	Generation string        `json:"generation"`
	Epoch      uint64        `json:"epoch"`
	Sequence   uint64        `json:"sequence"`
	ObservedAt time.Time     `json:"observed_at"`
	ReceivedAt time.Time     `json:"received_at"`
	ValidFor   time.Duration `json:"valid_for"`
}

type ApplyResult string

const (
	Applied      ApplyResult = "applied"
	IgnoredOlder ApplyResult = "ignored_older"
)

var (
	ErrUnknownLayer = errors.New("unknown state layer")
	ErrWrongOwner   = errors.New("state layer has a different owner")
	ErrInvalidFact  = errors.New("invalid state observation")
)

type Reducer struct {
	mu          sync.RWMutex
	definitions map[Layer]Definition
	facts       map[string]map[Layer]Observation
}

func NewReducer(definitions map[Layer]Definition) (*Reducer, error) {
	if len(definitions) == 0 {
		return nil, fmt.Errorf("%w: definitions are empty", ErrInvalidFact)
	}
	owned := make(map[Layer]Definition, len(definitions))
	for layer, definition := range definitions {
		if strings.TrimSpace(string(layer)) == "" || strings.TrimSpace(definition.Owner) == "" || definition.TTL <= 0 {
			return nil, fmt.Errorf("%w: bad definition for %q", ErrInvalidFact, layer)
		}
		owned[layer] = definition
	}
	return &Reducer{definitions: owned, facts: make(map[string]map[Layer]Observation)}, nil
}

func (r *Reducer) Apply(lineID string, observation Observation) (ApplyResult, error) {
	lineID = strings.TrimSpace(lineID)
	definition, ok := r.definitions[observation.Layer]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownLayer, observation.Layer)
	}
	if lineID == "" || observation.ObservedAt.IsZero() || observation.Sequence == 0 ||
		observation.ReceivedAt.IsZero() || observation.Epoch == 0 ||
		strings.TrimSpace(observation.Generation) == "" || strings.TrimSpace(observation.Source) == "" {
		return "", ErrInvalidFact
	}
	if !ValidCondition(observation.Condition) {
		return "", fmt.Errorf("%w: unknown condition %q", ErrInvalidFact, observation.Condition)
	}
	if observation.Code != "" && !ValidCode(observation.Code) {
		return "", fmt.Errorf("%w: invalid machine code", ErrInvalidFact)
	}
	if observation.Source != definition.Owner {
		return "", fmt.Errorf("%w: %s is owned by %s, got %s", ErrWrongOwner,
			observation.Layer, definition.Owner, observation.Source)
	}
	if observation.ValidFor <= 0 {
		observation.ValidFor = definition.TTL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	line := r.facts[lineID]
	if line == nil {
		line = make(map[Layer]Observation)
		r.facts[lineID] = line
	}
	current, exists := line[observation.Layer]
	if exists {
		if observation.Epoch < current.Epoch ||
			(observation.Epoch == current.Epoch && observation.Sequence <= current.Sequence) {
			return IgnoredOlder, nil
		}
	}
	line[observation.Layer] = observation
	return Applied, nil
}

func ValidCondition(condition Condition) bool {
	switch condition {
	case ConditionUnknown, ConditionInactive, ConditionStarting, ConditionReady,
		ConditionDegraded, ConditionBackoff, ConditionBlocked, ConditionFailed,
		ConditionActive:
		return true
	default:
		return false
	}
}

func ValidCode(value string) bool {
	if value == "" || len(value) > 96 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '_' && character != '-' && character != '.' && character != ':' {
			return false
		}
	}
	return true
}

type FactView struct {
	Layer      Layer     `json:"layer"`
	Condition  Condition `json:"condition"`
	Available  bool      `json:"available"`
	Fresh      bool      `json:"fresh"`
	Code       string    `json:"code,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Source     string    `json:"source,omitempty"`
	ProducerID string    `json:"producer_id,omitempty"`
	Generation string    `json:"generation,omitempty"`
	Epoch      uint64    `json:"epoch,omitempty"`
	Sequence   uint64    `json:"sequence,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
	ReceivedAt time.Time `json:"received_at,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

type LineView struct {
	LineID string     `json:"line_id"`
	Facts  []FactView `json:"facts"`
}

func (r *Reducer) View(lineID string, now time.Time) LineView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	view := LineView{LineID: lineID, Facts: make([]FactView, 0, len(r.definitions))}
	line := r.facts[lineID]
	layers := make([]Layer, 0, len(r.definitions))
	for layer := range r.definitions {
		layers = append(layers, layer)
	}
	sort.Slice(layers, func(i, j int) bool { return layers[i] < layers[j] })
	for _, layer := range layers {
		observation, exists := line[layer]
		if !exists {
			view.Facts = append(view.Facts, FactView{Layer: layer, Condition: ConditionUnknown})
			continue
		}
		expires := observation.ReceivedAt.Add(observation.ValidFor)
		fresh := now.Before(expires) || now.Equal(expires)
		fact := FactView{
			Layer: layer, Condition: observation.Condition, Available: observation.Available,
			Fresh: fresh, Code: observation.Code, Detail: observation.Detail,
			Source: observation.Source, ProducerID: observation.ProducerID,
			Generation: observation.Generation,
			Epoch:      observation.Epoch, Sequence: observation.Sequence,
			ObservedAt: observation.ObservedAt, ReceivedAt: observation.ReceivedAt,
			ExpiresAt: expires,
		}
		if !fresh {
			fact.Condition = ConditionUnknown
			fact.Available = false
			fact.Code = "stale"
			fact.Detail = "authoritative observation expired"
		}
		view.Facts = append(view.Facts, fact)
	}
	return view
}

type Requirement struct {
	Layer Layer
}

type Readiness struct {
	Ready   bool       `json:"ready"`
	Blocked []Layer    `json:"blocked,omitempty"`
	Facts   []FactView `json:"facts"`
}

func Evaluate(view LineView, requirements []Requirement) Readiness {
	byLayer := make(map[Layer]FactView, len(view.Facts))
	for _, fact := range view.Facts {
		byLayer[fact.Layer] = fact
	}
	result := Readiness{Ready: true, Facts: make([]FactView, 0, len(requirements))}
	for _, requirement := range requirements {
		fact, ok := byLayer[requirement.Layer]
		if !ok {
			fact = FactView{Layer: requirement.Layer, Condition: ConditionUnknown}
		}
		result.Facts = append(result.Facts, fact)
		if !fact.Fresh || !fact.Available {
			result.Ready = false
			result.Blocked = append(result.Blocked, requirement.Layer)
		}
	}
	return result
}
