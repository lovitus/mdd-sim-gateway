package events

import (
	"fmt"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

func TestAvailabilityFencesClockRollbackGenerationAndPurge(t *testing.T) {
	store, _ := openTestStore(t)
	at := time.Unix(1800100000, 0).UTC()
	layers := []state.Layer{state.LayerVoWiFiRuntime, state.LayerTunnel, state.LayerIMS}
	sequence := uint64(0)
	accept := func(second int64, generation string, full bool, condition state.Condition) {
		t.Helper()
		sequence++
		now := at.Add(time.Duration(second) * time.Second)
		var changes []Event
		if full {
			for _, layer := range layers {
				event := snapshotEvent(layer, "ready", sequence, now)
				event.Generation = generation
				event.EventID = fmt.Sprintf("availability-%s-%d-%s", generation, sequence, layer)
				changes = append(changes, event)
			}
		}
		if condition != "" {
			event := snapshotEvent(state.LayerIMS, "ims_failed", sequence, now)
			event.Generation, event.Condition, event.Available = generation, condition, false
			changes = append(changes, event)
		}
		cp := snapshotCheckpoint(generation, layers, sequence, now, now)
		if _, _, err := store.AcceptSnapshot(changes, cp); err != nil {
			t.Fatal(err)
		}
	}
	accept(0, "generation-1", true, "")
	accept(10, "generation-1", false, "")
	accept(5, "generation-1", false, state.ConditionFailed)
	accept(20, "generation-1", false, "")
	accept(30, "generation-1", false, "")
	history, err := store.Availability("line-1", at.Add(30*time.Second))
	if err != nil || history.Summary.Up != 10 || history.Summary.Down != 10 {
		t.Fatalf("rollback=%+v %v", history, err)
	}
	accept(40, "generation-2", true, "")
	accept(50, "generation-2", false, "")
	history, err = store.Availability("line-1", at.Add(50*time.Second))
	if err != nil || history.Summary.Up != 20 || history.Summary.Down != 10 {
		t.Fatalf("generation=%+v %v", history, err)
	}
	replay, err := NewReplay(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	purger, err := NewLinePurger(store, replay)
	if err != nil {
		t.Fatal(err)
	}
	if err := purger.PurgeLine("line-1"); err != nil {
		t.Fatal(err)
	}
	history, err = store.Availability("line-1", at.Add(60*time.Second))
	if err != nil || history.RecordedSince != nil || history.Summary.Observed != 0 {
		t.Fatalf("purge=%+v %v", history, err)
	}
	now := at.Add(70 * time.Second)
	if _, _, err := store.AcceptSnapshot(nil, snapshotCheckpoint("generation-2", layers, sequence+1, now, now)); err == nil {
		t.Fatal("purged history revived")
	}
}

func TestAvailabilityRetentionAndLongObservationGap(t *testing.T) {
	store, _ := openTestStore(t)
	at := time.Unix(1800100000, 0).UTC()
	layers := []state.Layer{state.LayerVoWiFiRuntime, state.LayerTunnel, state.LayerIMS}
	var changes []Event
	for _, layer := range layers {
		changes = append(changes, snapshotEvent(layer, "ready", 1, at))
	}
	cp := snapshotCheckpoint("generation-1", layers, 1, at, at)
	if _, _, err := store.AcceptSnapshot(changes, cp); err != nil {
		t.Fatal(err)
	}
	for i, seconds := range []int64{10, 100, 110, 172910, 172920} {
		cp.Sequence = uint64(i + 2)
		cp.ReceivedAt = at.Add(time.Duration(seconds) * time.Second)
		cp.ObservedAt = cp.ReceivedAt
		if _, _, err := store.AcceptSnapshot(nil, cp); err != nil {
			t.Fatal(err)
		}
		if seconds == 110 {
			history, err := store.Availability("line-1", cp.ReceivedAt)
			if err != nil || history.Summary.Up != 20 {
				t.Fatalf("gap=%+v %v", history, err)
			}
		}
	}
	history, err := store.Availability("line-1", cp.ReceivedAt)
	if err != nil || history.Summary.Up != 10 || history.RecordedSince == nil || *history.RecordedSince != at.Unix()+172910 {
		t.Fatalf("retention=%+v %v", history, err)
	}
}

func TestAvailabilityUsesReceiptsAndPreservesRestartGaps(t *testing.T) {
	store, path := openTestStore(t)
	if needed, err := store.AvailabilityNeedsSeed("line-1"); err != nil || !needed {
		t.Fatalf("new history seed=%v %v", needed, err)
	}
	at := time.Unix(1800100000, 0).UTC()
	layers := []state.Layer{state.LayerVoWiFiRuntime, state.LayerTunnel, state.LayerIMS}
	changes := make([]Event, 0, len(layers))
	for _, layer := range layers {
		changes = append(changes, snapshotEvent(layer, "ready", 1, at))
	}
	cp := snapshotCheckpoint("generation-1", layers, 1, at, at)
	if _, _, err := store.AcceptSnapshot(changes, cp); err != nil {
		t.Fatal(err)
	}
	if needed, err := store.AvailabilityNeedsSeed("line-1"); err != nil || needed {
		t.Fatalf("complete history seed=%v %v", needed, err)
	}
	cp.Sequence = 2
	cp.ReceivedAt = at.Add(10 * time.Second)
	cp.ObservedAt = cp.ReceivedAt
	if _, _, err := store.AcceptSnapshot(nil, cp); err != nil {
		t.Fatal(err)
	}
	got, err := store.Availability("line-1", at.Add(20*time.Second))
	if err != nil || got.Summary.Up != 10 || got.Summary.Observed != 10 {
		t.Fatalf("history=%+v err=%v", got, err)
	}
	// A duplicated snapshot carries no new evidence, even with a later receipt clock.
	cp.ReceivedAt = at.Add(20 * time.Second)
	if _, _, err := store.AcceptSnapshot(nil, cp); err != nil {
		t.Fatal(err)
	}
	got, err = store.Availability("line-1", at.Add(20*time.Second))
	if err != nil || got.Summary.Up != 10 {
		t.Fatalf("duplicate extended history: %+v %v", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cp.Sequence = 3
	cp.ObservedAt = cp.ReceivedAt
	if _, _, err := store.AcceptSnapshot(nil, cp); err != nil {
		t.Fatal(err)
	}
	cp.Sequence = 4
	cp.ReceivedAt = at.Add(30 * time.Second)
	cp.ObservedAt = cp.ReceivedAt
	if _, _, err := store.AcceptSnapshot(nil, cp); err != nil {
		t.Fatal(err)
	}
	got, err = store.Availability("line-1", cp.ReceivedAt)
	if err != nil || got.Summary.Up != 20 {
		t.Fatalf("restart gap fabricated: %+v %v", got, err)
	}
	_, summary := availabilityTimeline(got.Segments, at.Unix(), cp.ReceivedAt.Unix())
	if summary.Unknown != 10 {
		t.Fatalf("restart gap=%+v", summary)
	}
}

func TestAvailabilityTimelinePortsObservedOnlySummary(t *testing.T) {
	rows := []AvailabilitySegment{
		{State: "up", Start: 0, End: 10},
		{State: "down", Start: 10, End: 20, Reason: "tunnel_failed"},
		{State: "down", Start: 20, End: 30, Reason: "ims_failed"},
		{State: "off", Start: 40, End: 50},
	}
	_, got := availabilityTimeline(rows, 5, 60)
	if got.Up != 5 || got.Down != 20 || got.Off != 10 || got.Unknown != 20 || got.Outages != 1 || got.Longest != 20 || got.Ratio == nil || *got.Ratio != 0.2 {
		t.Fatalf("summary=%+v", got)
	}
	_, empty := availabilityTimeline(nil, 0, 60)
	if empty.Ratio != nil || empty.Unknown != 60 {
		t.Fatalf("empty=%+v", empty)
	}
}

func TestAvailabilityConditionDoesNotTreatIMSAloneAsConnectivity(t *testing.T) {
	layers := map[state.Layer]Event{
		state.LayerVoWiFiRuntime: {Condition: state.ConditionReady, Available: true},
		state.LayerIMS:           {Condition: state.ConditionReady, Available: true},
	}
	if got, _ := availabilityCondition(layers); got != "unknown" {
		t.Fatal(got)
	}
	layers[state.LayerTunnel] = Event{Condition: state.ConditionFailed, Code: "tunnel_failed"}
	if got, reason := availabilityCondition(layers); got != "down" || reason != "tunnel_failed" {
		t.Fatal(got, reason)
	}
	layers[state.LayerVoWiFiRuntime] = Event{Condition: state.ConditionInactive, Code: "runtime_stopped"}
	if got, _ := availabilityCondition(layers); got != "off" {
		t.Fatal(got)
	}
}
