package events

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/operations"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

func record(role ProducerRole, layer state.Layer, sequence uint64, available bool) Record {
	at := time.Unix(1_800_000_000+int64(sequence), 0).UTC()
	return Record{ReceivedAt: at, Epoch: 1, Event: Event{
		SchemaVersion: SchemaVersion, EventID: string(layer) + "-event", LineID: "line-1",
		ProducerRole: role, ProducerID: "producer-1", Layer: layer,
		Condition: state.ConditionReady, Available: available, Code: string(layer) + "_ready",
		Generation: "generation-1", Sequence: sequence, ObservedAt: at,
	}}
}

func TestLayerOwnerIsEnforcedAcrossProcesses(t *testing.T) {
	replay, err := NewReplay(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = replay.Apply(record(RoleCore, state.LayerIMS, 1, true))
	if !errors.Is(err, state.ErrWrongOwner) {
		t.Fatalf("error = %v, want ErrWrongOwner", err)
	}
}

func TestRecorderOwnsEpochAcrossProducerReplacement(t *testing.T) {
	recorder := NewRecorder()
	now := time.Unix(1_800_000_000, 0).UTC()
	first := record(RoleAgent, state.LayerHardware, 1, true).Event
	first.ProducerID = "agent-a"
	first.EventID = "first"
	if err := recorder.Authorize(first.LineID, first.ProducerRole, first.ProducerID, first.Generation); err != nil {
		t.Fatal(err)
	}
	firstRecord, err := recorder.Accept(first, now)
	if err != nil {
		t.Fatal(err)
	}
	replacement := first
	replacement.ProducerID = "agent-b"
	replacement.Generation = "session-b"
	replacement.EventID = "replacement"
	if err := recorder.Authorize(replacement.LineID, replacement.ProducerRole, replacement.ProducerID, replacement.Generation); err != nil {
		t.Fatal(err)
	}
	replacementRecord, err := recorder.Accept(replacement, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	lateOld := first
	lateOld.Sequence = 99
	lateOld.EventID = "late-old"
	if _, err := recorder.Accept(lateOld, now.Add(2*time.Second)); !errors.Is(err, ErrUnauthorizedProducer) {
		t.Fatalf("late old error = %v", err)
	}
	if firstRecord.Epoch != 1 || replacementRecord.Epoch != 2 {
		t.Fatalf("epochs = first:%d replacement:%d", firstRecord.Epoch, replacementRecord.Epoch)
	}
}

func TestRecorderRequiresExplicitProducerBinding(t *testing.T) {
	recorder := NewRecorder()
	event := record(RoleVoWiFi, state.LayerIMS, 1, true).Event
	if _, err := recorder.Accept(event, time.Now()); !errors.Is(err, ErrUnauthorizedProducer) {
		t.Fatalf("error = %v", err)
	}
}

func TestDirectEventsKeepCellularCapabilitiesIndependent(t *testing.T) {
	replay, err := NewReplay(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		record(RoleCore, state.LayerIntent, 1, true),
		record(RoleAgent, state.LayerAgentLink, 1, true),
		record(RoleAgent, state.LayerHardware, 1, true),
		record(RoleAgent, state.LayerCard, 1, true),
		record(RoleAgent, state.LayerCellularData, 1, true),
	}
	for _, item := range records {
		if _, err := replay.Apply(item); err != nil {
			t.Fatal(err)
		}
	}
	projection := replay.Projections(replay.LastReceivedAt())[0]
	if !projection.Operations[operations.CellularData].Ready ||
		projection.Operations[operations.CellularCall].Ready ||
		projection.Operations[operations.CellularSMS].Ready {
		t.Fatalf("operations = %+v", projection.Operations)
	}
}

func TestOldGenerationCannotReturnThroughReplay(t *testing.T) {
	replay, err := NewReplay(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	old := record(RoleVoWiFi, state.LayerIMS, 1, true)
	if _, err := replay.Apply(old); err != nil {
		t.Fatal(err)
	}
	current := record(RoleVoWiFi, state.LayerIMS, 1, false)
	current.Event.Generation = "generation-2"
	current.Epoch = 2
	current.Event.Condition = state.ConditionDegraded
	if _, err := replay.Apply(current); err != nil {
		t.Fatal(err)
	}
	old.Event.Sequence = 99
	old.ReceivedAt = old.ReceivedAt.Add(time.Minute)
	if result, err := replay.Apply(old); err != nil || result != state.IgnoredOlder {
		t.Fatalf("late old result = %q, error = %v", result, err)
	}
	fact := findLayer(t, replay.Projections(old.ReceivedAt)[0], state.LayerIMS)
	if fact.Generation != "generation-2" || fact.Available {
		t.Fatalf("current fact was replaced: %+v", fact)
	}
}

func TestJSONLinesRejectUnknownFields(t *testing.T) {
	replay, err := NewReplay(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	input := `{"received_at":"2027-01-15T08:00:01Z","unknown":true,"event":{}}`
	if err := ReadJSONLines(strings.NewReader(input), replay, DefaultMaxRecordBytes); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error = %v", err)
	}
}

func findLayer(t *testing.T, projection LineProjection, layer state.Layer) state.FactView {
	t.Helper()
	for _, fact := range projection.Facts {
		if fact.Layer == layer {
			return fact
		}
	}
	t.Fatalf("missing layer %s", layer)
	return state.FactView{}
}
