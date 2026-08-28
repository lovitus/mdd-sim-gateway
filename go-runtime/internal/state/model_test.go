package state

import (
	"errors"
	"testing"
	"time"
)

func testReducer(t *testing.T) *Reducer {
	t.Helper()
	reducer, err := NewReducer(map[Layer]Definition{
		LayerHardware: {Owner: "agent-a", TTL: 10 * time.Second},
		LayerTunnel:   {Owner: "engine-1", TTL: 5 * time.Second},
		LayerIMS:      {Owner: "engine-1", TTL: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reducer
}

func observation(layer Layer, source string, sequence uint64, at time.Time) Observation {
	return Observation{Layer: layer, Condition: ConditionReady, Available: true,
		Source: source, Generation: "generation-1", Epoch: 1, Sequence: sequence,
		ObservedAt: at, ReceivedAt: at}
}

func TestOlderGenerationCannotReturnAfterReplacement(t *testing.T) {
	reducer := testReducer(t)
	now := time.Now()
	newGeneration := observation(LayerIMS, "engine-1", 1, now)
	newGeneration.Generation = "generation-2"
	newGeneration.Epoch = 2
	if _, err := reducer.Apply("line-1", newGeneration); err != nil {
		t.Fatal(err)
	}
	lateOld := observation(LayerIMS, "engine-1", 999, now.Add(time.Minute))
	lateOld.ReceivedAt = now.Add(time.Minute)
	if result, err := reducer.Apply("line-1", lateOld); err != nil || result != IgnoredOlder {
		t.Fatalf("late old generation = %q, %v", result, err)
	}
	if got := Evaluate(reducer.View("line-1", now.Add(time.Second)),
		[]Requirement{{Layer: LayerIMS}}); !got.Ready || got.Facts[0].Epoch != 2 {
		t.Fatalf("readiness = %+v", got)
	}
}

func TestLayerOwnerCannotBeOverwritten(t *testing.T) {
	reducer := testReducer(t)
	_, err := reducer.Apply("line-1", observation(LayerHardware, "engine-1", 1, time.Now()))
	if !errors.Is(err, ErrWrongOwner) {
		t.Fatalf("error = %v, want ErrWrongOwner", err)
	}
}

func TestUnknownConditionIsRejected(t *testing.T) {
	reducer := testReducer(t)
	fact := observation(LayerHardware, "agent-a", 1, time.Now())
	fact.Condition = "translated_display_label"
	if _, err := reducer.Apply("line-1", fact); !errors.Is(err, ErrInvalidFact) {
		t.Fatalf("error = %v, want ErrInvalidFact", err)
	}
}

func TestDisplayTextCannotBeUsedAsMachineCode(t *testing.T) {
	reducer := testReducer(t)
	fact := observation(LayerHardware, "agent-a", 1, time.Now())
	fact.Code = "Voice is working"
	if _, err := reducer.Apply("line-1", fact); !errors.Is(err, ErrInvalidFact) {
		t.Fatalf("error = %v, want ErrInvalidFact", err)
	}
}

func TestOlderObservationCannotReplaceCurrentFact(t *testing.T) {
	reducer := testReducer(t)
	now := time.Now()
	if result, err := reducer.Apply("line-1", observation(LayerIMS, "engine-1", 2, now)); err != nil || result != Applied {
		t.Fatalf("first apply = %q, %v", result, err)
	}
	if result, err := reducer.Apply("line-1", observation(LayerIMS, "engine-1", 1, now.Add(time.Second))); err != nil || result != IgnoredOlder {
		t.Fatalf("older apply = %q, %v", result, err)
	}
	view := reducer.View("line-1", now.Add(2*time.Second))
	if got := Evaluate(view, []Requirement{{Layer: LayerIMS}}); !got.Ready || got.Facts[0].Sequence != 2 {
		t.Fatalf("readiness = %+v", got)
	}
}

func TestStaleFactBecomesUnknownWithoutInventingFailure(t *testing.T) {
	reducer := testReducer(t)
	now := time.Now()
	if _, err := reducer.Apply("line-1", observation(LayerTunnel, "engine-1", 1, now)); err != nil {
		t.Fatal(err)
	}
	readiness := Evaluate(reducer.View("line-1", now.Add(6*time.Second)),
		[]Requirement{{Layer: LayerTunnel}})
	if readiness.Ready || readiness.Facts[0].Condition != ConditionUnknown ||
		readiness.Facts[0].Code != "stale" {
		t.Fatalf("readiness = %+v", readiness)
	}
}

func TestFreshnessUsesServerReceiveTimeNotRemoteClock(t *testing.T) {
	reducer := testReducer(t)
	now := time.Now()
	fact := observation(LayerTunnel, "engine-1", 1, now.Add(-24*time.Hour))
	fact.ReceivedAt = now
	if _, err := reducer.Apply("line-1", fact); err != nil {
		t.Fatal(err)
	}
	if got := Evaluate(reducer.View("line-1", now.Add(4*time.Second)),
		[]Requirement{{Layer: LayerTunnel}}); !got.Ready {
		t.Fatalf("fresh server receipt was rejected due to remote clock: %+v", got)
	}
}

func TestCompleteSnapshotCheckpointRefreshesOnlyExactProducerGeneration(t *testing.T) {
	reducer, err := NewReducer(map[Layer]Definition{
		LayerHardware: {Owner: "mdd-agent", TTL: 5 * time.Second},
		LayerTunnel:   {Owner: "mdd-vowifi", TTL: 5 * time.Second},
		LayerMedia:    {Owner: "mdd-vowifi", TTL: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	current := observation(LayerTunnel, "generation-1", 1, now)
	current.Source = "mdd-vowifi"
	current.ProducerID = "provider-1"
	if _, err := reducer.Apply("line-1", current); err != nil {
		t.Fatal(err)
	}
	notCovered := current
	notCovered.Layer = LayerMedia
	if _, err := reducer.Apply("line-1", notCovered); err != nil {
		t.Fatal(err)
	}
	other := observation(LayerHardware, "agent-generation", 1, now)
	other.Source = "mdd-agent"
	other.ProducerID = "agent-1"
	if _, err := reducer.Apply("line-1", other); err != nil {
		t.Fatal(err)
	}
	confirmedAt := now.Add(4 * time.Second)
	if err := reducer.Confirm("line-1", "mdd-vowifi", "provider-1", "generation-1", []Layer{LayerTunnel}, 2, confirmedAt); err != nil {
		t.Fatal(err)
	}
	view := reducer.View("line-1", confirmedAt.Add(2*time.Second))
	byLayer := make(map[Layer]FactView)
	for _, fact := range view.Facts {
		byLayer[fact.Layer] = fact
	}
	if byLayer[LayerTunnel].ReceivedAt != confirmedAt || !byLayer[LayerTunnel].Fresh {
		t.Fatalf("provider fact=%+v", byLayer[LayerTunnel])
	}
	if byLayer[LayerHardware].ReceivedAt != now || byLayer[LayerHardware].Fresh {
		t.Fatalf("unrelated Agent fact was refreshed: %+v", byLayer[LayerHardware])
	}
	if byLayer[LayerMedia].ReceivedAt != now || byLayer[LayerMedia].Fresh {
		t.Fatalf("provider fact outside checkpoint coverage was refreshed: %+v", byLayer[LayerMedia])
	}
	if err := reducer.Confirm("line-1", "mdd-vowifi", "provider-1", "generation-1", []Layer{LayerTunnel}, 1, confirmedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := reducer.View("line-1", confirmedAt.Add(time.Second)); factForLayer(got, LayerTunnel).ReceivedAt != confirmedAt {
		t.Fatal("older checkpoint refreshed a fact")
	}
}

func factForLayer(view LineView, layer Layer) FactView {
	for _, fact := range view.Facts {
		if fact.Layer == layer {
			return fact
		}
	}
	return FactView{}
}

func TestOperationReadinessUsesOnlyItsRequiredLayers(t *testing.T) {
	reducer := testReducer(t)
	now := time.Now()
	for _, fact := range []Observation{
		observation(LayerHardware, "agent-a", 1, now),
		observation(LayerTunnel, "engine-1", 1, now),
	} {
		if _, err := reducer.Apply("line-1", fact); err != nil {
			t.Fatal(err)
		}
	}
	if got := Evaluate(reducer.View("line-1", now), []Requirement{{Layer: LayerHardware}}); !got.Ready {
		t.Fatalf("hardware-only operation unexpectedly blocked: %+v", got)
	}
	if got := Evaluate(reducer.View("line-1", now), []Requirement{{Layer: LayerTunnel}, {Layer: LayerIMS}}); got.Ready {
		t.Fatalf("VoWiFi operation unexpectedly ready: %+v", got)
	}
}
