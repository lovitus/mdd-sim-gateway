package legacy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

func boolPointer(value bool) *bool { return &value }

func completeSnapshot() Snapshot {
	return Snapshot{
		Instances: []Instance{{
			ID: "7", Enabled: boolPointer(true),
			Facts: LegacyLineFacts{
				SampledAt:  1_700_000_000,
				Generation: LegacyGeneration{ContainerID: "container-a", EngineRunID: "run-a", VPCDSessionGeneration: "session-a"},
				Facts: map[string]LegacyFact{
					"engine":     {State: "ready", Code: "engine_running"},
					"card_route": {State: "ready", Code: "card_route_current"},
					"pin":        {State: "ready", Code: "pin_usable"},
					"tunnel":     {State: "ready", Code: "tunnel_installed"},
					"ims":        {State: "ready", Code: "sip_registered"},
					"admission":  {State: "ready", Code: "admission_allowed"},
					"messaging":  {State: "ready", Code: "messaging_ready"},
					"media":      {State: "ready", Code: "browser_media_ready"},
					"work":       {State: "ready", Code: "idle"},
				},
			},
		}},
		Devices: []Device{{
			ID: "modem-a", InstanceID: "7", AgentID: "agent-a", Present: true,
			SIM: SIM{Present: true},
			Capabilities: map[string]Capability{
				"cellular": {Actual: "on", Available: boolPointer(true)},
				"call":     {Actual: "on", Available: boolPointer(true)},
				"sms":      {Actual: "on", Available: boolPointer(true)},
			},
		}},
	}
}

func translate(t *testing.T, snapshot Snapshot) LineProjection {
	t.Helper()
	translator, err := NewTranslator(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := translator.Translate(snapshot, time.Unix(1_800_000_000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	return lines[0]
}

func TestCompleteLegacyFactsDoNotInventDurableVoWiFiIntent(t *testing.T) {
	line := translate(t, completeSnapshot())
	for _, name := range []string{"cellular_data", "cellular_call", "cellular_sms"} {
		if readiness := line.Operations[name]; !readiness.Ready {
			t.Errorf("operation %s unexpectedly blocked: %+v", name, readiness.Blocked)
		}
	}
	for _, name := range []string{"vowifi_call", "vowifi_sms"} {
		readiness := line.Operations[name]
		if readiness.Ready || len(readiness.Blocked) != 1 || readiness.Blocked[0] != state.LayerVoWiFiIntent {
			t.Errorf("operation %s readiness=%+v, want only durable VoWiFi intent blocked", name, readiness)
		}
	}
}

func TestDisplayLabelCannotFabricateIMSReadiness(t *testing.T) {
	snapshot := completeSnapshot()
	delete(snapshot.Instances[0].Facts.Facts, "ims")
	snapshot.Instances[0].Status.Label = "Working"
	line := translate(t, snapshot)
	if line.Operations["vowifi_call"].Ready || line.Operations["vowifi_sms"].Ready {
		t.Fatalf("VoWiFi became ready without an IMS machine fact: %+v", line.Operations)
	}
	if fact := findFact(t, line, state.LayerIMS); fact.Condition != state.ConditionUnknown {
		t.Fatalf("IMS fact = %+v, want unknown", fact)
	}
}

func TestReturningOldGenerationCannotReplaceCurrentFacts(t *testing.T) {
	translator, err := NewTranslator(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	oldSnapshot := completeSnapshot()
	if _, err := translator.Translate(oldSnapshot, now); err != nil {
		t.Fatal(err)
	}
	newSnapshot := completeSnapshot()
	newSnapshot.Instances[0].Facts.Generation.EngineRunID = "run-b"
	newSnapshot.Instances[0].Facts.Facts["ims"] = LegacyFact{State: "degraded", Code: "sip_rejected"}
	current, err := translator.Translate(newSnapshot, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	late, err := translator.Translate(oldSnapshot, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if late[0].Generation != current[0].Generation {
		t.Fatalf("late generation = %q, current = %q", late[0].Generation, current[0].Generation)
	}
	if fact := findFact(t, late[0], state.LayerIMS); fact.Code != "sip_rejected" || fact.Epoch != 2 {
		t.Fatalf("old generation replaced current IMS fact: %+v", fact)
	}
}

func TestCellularDataDoesNotFabricateVoiceOrSMS(t *testing.T) {
	snapshot := completeSnapshot()
	delete(snapshot.Devices[0].Capabilities, "call")
	delete(snapshot.Devices[0].Capabilities, "sms")
	line := translate(t, snapshot)
	if !line.Operations["cellular_data"].Ready {
		t.Fatalf("cellular data unexpectedly blocked: %+v", line.Operations["cellular_data"])
	}
	if line.Operations["cellular_call"].Ready || line.Operations["cellular_sms"].Ready {
		t.Fatalf("data readiness fabricated call/SMS: %+v", line.Operations)
	}
}

func TestDegradedMachineFactIsUnavailable(t *testing.T) {
	snapshot := completeSnapshot()
	snapshot.Instances[0].Facts.Facts["tunnel"] = LegacyFact{State: "degraded", Code: "tunnel_down"}
	line := translate(t, snapshot)
	if line.Operations["vowifi_call"].Ready {
		t.Fatal("degraded tunnel was treated as available")
	}
	fact := findFact(t, line, state.LayerTunnel)
	if fact.Condition != state.ConditionDegraded || fact.Available {
		t.Fatalf("tunnel fact = %+v", fact)
	}
}

func TestGenerationChangeAdvancesEpoch(t *testing.T) {
	translator, err := NewTranslator(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := completeSnapshot()
	now := time.Unix(1_800_000_000, 0).UTC()
	first, err := translator.Translate(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Instances[0].Facts.Generation.EngineRunID = "run-b"
	second, err := translator.Translate(snapshot, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	firstEpoch := findFact(t, first[0], state.LayerIntent).Epoch
	secondEpoch := findFact(t, second[0], state.LayerIntent).Epoch
	if secondEpoch != firstEpoch+1 {
		t.Fatalf("epochs = %d then %d", firstEpoch, secondEpoch)
	}
}

func TestDecodeRequiresBothSnapshotCollections(t *testing.T) {
	for _, input := range []string{`{"instances":[]}`, `{"devices":[]}`} {
		if _, err := Decode(strings.NewReader(input)); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("Decode(%s) error = %v", input, err)
		}
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	if _, err := Decode(strings.NewReader(`{"instances":[],"devices":[]} {}`)); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeAcceptsNumericInstanceID(t *testing.T) {
	snapshot, err := Decode(strings.NewReader(`{"instances":[{"id":7}],"devices":[{"id":"m","instance_id":7}]}`))
	if err != nil {
		t.Fatal(err)
	}
	line := translate(t, snapshot)
	if line.LineID != "7" {
		t.Fatalf("line id = %q", line.LineID)
	}
}

func findFact(t *testing.T, line LineProjection, layer state.Layer) state.FactView {
	t.Helper()
	for _, fact := range line.Facts {
		if fact.Layer == layer {
			return fact
		}
	}
	t.Fatalf("missing layer %s", layer)
	return state.FactView{}
}
