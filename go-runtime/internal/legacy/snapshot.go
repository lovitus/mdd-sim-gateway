// Package legacy converts a saved legacy /api/snapshot response into typed Go
// state facts. It is deliberately read-only: this package has no network,
// hardware, messaging, call, or recovery client.
package legacy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/operations"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

const Source = "legacy.snapshot"

var ErrInvalidSnapshot = errors.New("invalid legacy snapshot")

type Snapshot struct {
	Instances []Instance `json:"instances"`
	Devices   []Device   `json:"devices"`
}

type Instance struct {
	ID      any             `json:"id"`
	Enabled *bool           `json:"enabled,omitempty"`
	Status  LegacyStatus    `json:"status"`
	Facts   LegacyLineFacts `json:"facts"`
}

// LegacyStatus is decoded only to prove that presentation labels are not used
// as machine facts by the translator.
type LegacyStatus struct {
	Label string `json:"label"`
}

type LegacyLineFacts struct {
	SampledAt  int64                 `json:"sampled_at"`
	Generation LegacyGeneration      `json:"generation"`
	Facts      map[string]LegacyFact `json:"facts"`
}

type LegacyGeneration struct {
	ContainerID           string `json:"container_id"`
	EngineRunID           string `json:"engine_run_id"`
	VPCDSessionGeneration string `json:"vpcd_session_generation"`
}

type LegacyFact struct {
	State      string `json:"state"`
	Code       string `json:"code"`
	ObservedAt int64  `json:"observed_at"`
}

type Device struct {
	ID           string                `json:"id"`
	InstanceID   any                   `json:"instance_id"`
	AgentID      string                `json:"agent_id"`
	Present      bool                  `json:"present"`
	SIM          SIM                   `json:"sim"`
	Capabilities map[string]Capability `json:"capabilities"`
}

type SIM struct {
	Present bool `json:"present"`
}

type Capability struct {
	Actual    string `json:"actual"`
	Available *bool  `json:"available,omitempty"`
}

type LineProjection struct {
	LineID     string                     `json:"line_id"`
	Generation string                     `json:"generation"`
	Facts      []state.FactView           `json:"facts"`
	Operations map[string]state.Readiness `json:"operations"`
}

type lineClock struct {
	Fingerprint string
	Epoch       uint64
	Sequence    uint64
	Seen        map[string]uint64
}

type Translator struct {
	mu      sync.Mutex
	ttl     time.Duration
	reducer *state.Reducer
	clocks  map[string]lineClock
}

func Decode(reader io.Reader) (Snapshot, error) {
	var envelope map[string]json.RawMessage
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Snapshot{}, fmt.Errorf("%w: trailing input: %v", ErrInvalidSnapshot, err)
	}
	instances, instancesOK := envelope["instances"]
	devices, devicesOK := envelope["devices"]
	if !instancesOK || !devicesOK {
		return Snapshot{}, fmt.Errorf("%w: both instances and devices are required", ErrInvalidSnapshot)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(instances, &snapshot.Instances); err != nil {
		return Snapshot{}, fmt.Errorf("%w: instances: %v", ErrInvalidSnapshot, err)
	}
	if err := json.Unmarshal(devices, &snapshot.Devices); err != nil {
		return Snapshot{}, fmt.Errorf("%w: devices: %v", ErrInvalidSnapshot, err)
	}
	return snapshot, nil
}

func NewTranslator(ttl time.Duration) (*Translator, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("%w: ttl must be positive", ErrInvalidSnapshot)
	}
	definitions := make(map[state.Layer]state.Definition)
	for _, layer := range allLayers() {
		definitions[layer] = state.Definition{Owner: Source, TTL: ttl}
	}
	reducer, err := state.NewReducer(definitions)
	if err != nil {
		return nil, err
	}
	return &Translator{ttl: ttl, reducer: reducer, clocks: make(map[string]lineClock)}, nil
}

func (t *Translator) Translate(snapshot Snapshot, receivedAt time.Time) ([]LineProjection, error) {
	if receivedAt.IsZero() {
		return nil, fmt.Errorf("%w: receive time is required", ErrInvalidSnapshot)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	devices := make(map[string]Device)
	for _, device := range snapshot.Devices {
		lineID := normalizeID(device.InstanceID)
		if lineID != "" {
			if _, duplicate := devices[lineID]; duplicate {
				return nil, fmt.Errorf("%w: multiple devices claim instance %q", ErrInvalidSnapshot, lineID)
			}
			devices[lineID] = device
		}
	}
	result := make([]LineProjection, 0, len(snapshot.Instances))
	seen := make(map[string]struct{}, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		lineID := normalizeID(instance.ID)
		if lineID == "" {
			return nil, fmt.Errorf("%w: instance id is empty", ErrInvalidSnapshot)
		}
		if _, duplicate := seen[lineID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate instance %q", ErrInvalidSnapshot, lineID)
		}
		seen[lineID] = struct{}{}
		fingerprint := generationFingerprint(lineID, instance.Facts.Generation)
		clock := t.clocks[lineID]
		if clock.Seen == nil {
			clock.Seen = make(map[string]uint64)
		}
		incomingEpoch, seenGeneration := clock.Seen[fingerprint]
		if clock.Epoch == 0 {
			clock.Epoch = 1
			clock.Fingerprint = fingerprint
			incomingEpoch = clock.Epoch
			clock.Seen[fingerprint] = incomingEpoch
		} else if clock.Fingerprint == fingerprint {
			incomingEpoch = clock.Epoch
		} else if !seenGeneration {
			clock.Epoch++
			clock.Fingerprint = fingerprint
			incomingEpoch = clock.Epoch
			clock.Seen[fingerprint] = incomingEpoch
		}
		clock.Sequence++
		t.clocks[lineID] = clock

		facts := translateInstance(instance, devices[lineID], receivedAt)
		for _, fact := range facts {
			fact.Source = Source
			fact.ProducerID = "legacy-shadow"
			fact.Generation = fingerprint
			fact.Epoch = incomingEpoch
			fact.Sequence = clock.Sequence
			fact.ReceivedAt = receivedAt
			fact.ValidFor = t.ttl
			if fact.ObservedAt.IsZero() {
				fact.ObservedAt = receivedAt
			}
			if _, err := t.reducer.Apply(lineID, fact); err != nil {
				return nil, fmt.Errorf("translate line %s layer %s: %w", lineID, fact.Layer, err)
			}
		}

		view := t.reducer.View(lineID, receivedAt)
		result = append(result, LineProjection{
			LineID: lineID, Generation: clock.Fingerprint, Facts: view.Facts,
			Operations: operations.EvaluateAll(view),
		})
	}
	return result, nil
}

func allLayers() []state.Layer {
	return []state.Layer{
		state.LayerIntent, state.LayerVoWiFiIntent, state.LayerAgentLink, state.LayerHardware, state.LayerCard,
		state.LayerCardRoute, state.LayerPIN, state.LayerCellularData,
		state.LayerCellularVoice, state.LayerCellularSMS, state.LayerEngineProcess,
		state.LayerVoWiFiRuntime, state.LayerTunnel, state.LayerIMS, state.LayerIMSVoice,
		state.LayerAdmission, state.LayerMessaging,
		state.LayerMedia, state.LayerCall,
	}
}

func translateInstance(instance Instance, device Device, receivedAt time.Time) []state.Observation {
	enabled := instance.Enabled == nil || *instance.Enabled
	facts := []state.Observation{observation(
		state.LayerIntent, condition(enabled, state.ConditionReady, state.ConditionInactive),
		enabled, code(enabled, "line_enabled", "line_disabled"), receivedAt,
	)}
	if device.ID != "" {
		facts = append(facts,
			observation(state.LayerHardware,
				condition(device.Present, state.ConditionReady, state.ConditionInactive),
				device.Present, code(device.Present, "hardware_present", "hardware_absent"), receivedAt),
			observation(state.LayerCard,
				condition(device.SIM.Present, state.ConditionReady, state.ConditionInactive),
				device.SIM.Present, code(device.SIM.Present, "card_present", "card_absent"), receivedAt),
		)
		if strings.TrimSpace(device.AgentID) != "" {
			facts = append(facts, observation(state.LayerAgentLink,
				condition(device.Present, state.ConditionReady, state.ConditionInactive),
				device.Present, code(device.Present, "agent_device_online", "agent_device_offline"), receivedAt))
		} else {
			facts = append(facts, observation(state.LayerAgentLink,
				condition(device.Present, state.ConditionReady, state.ConditionInactive),
				device.Present, code(device.Present, "legacy_local_hardware_scope", "legacy_local_hardware_absent"), receivedAt))
		}
		facts = appendCapability(facts, state.LayerCellularData, "cellular_data", device.Capabilities["cellular"], receivedAt)
		facts = appendCapability(facts, state.LayerCellularVoice, "cellular_voice", device.Capabilities["call"], receivedAt)
		facts = appendCapability(facts, state.LayerCellularSMS, "cellular_sms", device.Capabilities["sms"], receivedAt)
	}
	legacyLayers := map[string]state.Layer{
		"engine": state.LayerEngineProcess, "card_route": state.LayerCardRoute,
		"pin": state.LayerPIN, "tunnel": state.LayerTunnel, "ims": state.LayerIMS,
		"admission": state.LayerAdmission, "messaging": state.LayerMessaging,
		"media": state.LayerMedia, "work": state.LayerCall,
	}
	for name, layer := range legacyLayers {
		if fact, ok := instance.Facts.Facts[name]; ok {
			facts = append(facts, translateLegacyFact(layer, fact, instance.Facts.SampledAt, receivedAt))
		}
	}
	return facts
}

func appendCapability(facts []state.Observation, layer state.Layer, name string, capability Capability, at time.Time) []state.Observation {
	if capability.Actual == "" && capability.Available == nil {
		return facts
	}
	conditionValue, available := capabilityState(capability)
	return append(facts, observation(layer, conditionValue, available, name+"_"+string(conditionValue), at))
}

func capabilityState(capability Capability) (state.Condition, bool) {
	actual := strings.ToLower(strings.TrimSpace(capability.Actual))
	declaredAvailable := capability.Available == nil || *capability.Available
	switch actual {
	case "on", "ready", "working", "active":
		if declaredAvailable {
			return state.ConditionReady, true
		}
		return state.ConditionDegraded, false
	case "starting", "stopping":
		return state.ConditionStarting, false
	case "degraded", "error", "failed":
		return state.ConditionDegraded, false
	case "off", "inactive", "stopped":
		return state.ConditionInactive, false
	case "unsupported", "blocked":
		return state.ConditionBlocked, false
	default:
		return state.ConditionUnknown, false
	}
}

func translateLegacyFact(layer state.Layer, fact LegacyFact, sampledAt int64, fallback time.Time) state.Observation {
	conditionValue := legacyCondition(fact.State)
	observedAt := fallback
	if fact.ObservedAt > 0 {
		observedAt = time.Unix(fact.ObservedAt, 0).UTC()
	} else if sampledAt > 0 {
		observedAt = time.Unix(sampledAt, 0).UTC()
	}
	codeValue := strings.TrimSpace(fact.Code)
	if !state.ValidCode(codeValue) {
		codeValue = ""
	}
	if codeValue == "" {
		codeValue = "legacy_" + string(conditionValue)
	}
	return observation(layer, conditionValue,
		conditionValue == state.ConditionReady || conditionValue == state.ConditionActive,
		codeValue, observedAt)
}

func legacyCondition(value string) state.Condition {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ready":
		return state.ConditionReady
	case "degraded":
		return state.ConditionDegraded
	case "blocked":
		return state.ConditionBlocked
	case "active":
		return state.ConditionActive
	case "inactive":
		return state.ConditionInactive
	default:
		return state.ConditionUnknown
	}
}

func observation(layer state.Layer, conditionValue state.Condition, available bool, codeValue string, observedAt time.Time) state.Observation {
	return state.Observation{Layer: layer, Condition: conditionValue, Available: available, Code: codeValue, ObservedAt: observedAt}
}

func generationFingerprint(lineID string, generation LegacyGeneration) string {
	parts := []string{strings.TrimSpace(generation.EngineRunID), strings.TrimSpace(generation.ContainerID), strings.TrimSpace(generation.VPCDSessionGeneration)}
	if strings.Join(parts, "") == "" {
		return "legacy:" + lineID + ":unversioned"
	}
	return "legacy:" + lineID + ":" + strings.Join(parts, ":")
}

func normalizeID(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
	}
	return ""
}

func condition(value bool, yes, no state.Condition) state.Condition {
	if value {
		return yes
	}
	return no
}

func code(value bool, yes, no string) string {
	if value {
		return yes
	}
	return no
}
