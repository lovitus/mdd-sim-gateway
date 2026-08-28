package events

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	bolt "go.etcd.io/bbolt"
)

func openTestStore(t *testing.T) (*BoltStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.db")
	store, err := OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestBoltStoreActivationAndEpochSurviveRestart(t *testing.T) {
	store, path := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	first := record(RoleAgent, state.LayerHardware, 1, true).Event
	first.EventID = "first"
	first.ProducerID = "agent-a"
	first.Generation = "generation-a"
	firstRecord, err := store.Activate(first, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	replacement := first
	replacement.EventID = "replacement"
	replacement.ProducerID = "agent-b"
	replacement.Generation = "generation-b"
	replacement.Sequence = 1
	replacementRecord, err := store.Activate(replacement, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	lateOld := first
	lateOld.EventID = "late-old"
	lateOld.Sequence = 99
	if _, err := store.Accept(lateOld, now.Add(2*time.Second)); !errors.Is(err, ErrUnauthorizedProducer) {
		t.Fatalf("late producer error = %v", err)
	}
	if firstRecord.Epoch != 1 || replacementRecord.Epoch != 2 {
		t.Fatalf("epochs = %d then %d", firstRecord.Epoch, replacementRecord.Epoch)
	}
}

func TestBoltStoreFailedActivationDoesNotReplaceBinding(t *testing.T) {
	store, _ := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	current := record(RoleAgent, state.LayerHardware, 1, true).Event
	current.EventID = "current"
	if _, err := store.Activate(current, now); err != nil {
		t.Fatal(err)
	}
	invalidReplacement := current
	invalidReplacement.EventID = "invalid"
	invalidReplacement.ProducerID = "replacement"
	invalidReplacement.Generation = "replacement"
	invalidReplacement.Code = "not a valid machine code"
	if _, err := store.Activate(invalidReplacement, now.Add(time.Second)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid replacement error = %v", err)
	}
	current.EventID = "still-current"
	current.Sequence++
	if _, err := store.Accept(current, now.Add(2*time.Second)); err != nil {
		t.Fatalf("failed transaction changed binding: %v", err)
	}
}

func TestBoltStoreRejectedGenerationReuseRollsBackBinding(t *testing.T) {
	store, _ := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	first := record(RoleAgent, state.LayerHardware, 1, true).Event
	first.EventID = "first-generation"
	first.ProducerID = "agent-a"
	first.Generation = "generation-a"
	if _, err := store.Activate(first, now); err != nil {
		t.Fatal(err)
	}
	replacement := first
	replacement.EventID = "replacement-generation"
	replacement.ProducerID = "agent-b"
	replacement.Generation = "generation-b"
	if _, err := store.Activate(replacement, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	reused := first
	reused.EventID = "reused-generation"
	reused.Sequence++
	if _, err := store.Activate(reused, now.Add(2*time.Second)); !errors.Is(err, ErrGenerationReused) {
		t.Fatalf("generation reuse error = %v", err)
	}
	replacement.EventID = "replacement-remains-current"
	replacement.Sequence++
	if _, err := store.Accept(replacement, now.Add(3*time.Second)); err != nil {
		t.Fatalf("failed activation changed the binding: %v", err)
	}
}

func TestBoltStoreEventIDIsIdempotentAndCollisionSafe(t *testing.T) {
	store, _ := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	event := record(RoleAgent, state.LayerHardware, 1, true).Event
	event.EventID = "idempotent"
	first, err := store.Activate(event, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Accept(event, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("retry changed record: first=%+v second=%+v", first, second)
	}
	collision := event
	collision.Sequence++
	if _, err := store.Accept(collision, now.Add(time.Minute)); !errors.Is(err, ErrEventIDConflict) {
		t.Fatalf("collision error = %v", err)
	}
	count, err := store.Count()
	if err != nil || count != 1 {
		t.Fatalf("count=%d error=%v", count, err)
	}
}

func TestBoltStoreReplaysAndExportsSameRecords(t *testing.T) {
	store, _ := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	event := record(RoleAgent, state.LayerHardware, 1, true).Event
	if _, err := store.Activate(event, now); err != nil {
		t.Fatal(err)
	}
	replay, err := NewReplay(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplayInto(replay); err != nil {
		t.Fatal(err)
	}
	got := replay.Projections(now)
	if len(got) != 1 {
		t.Fatalf("projection count = %d", len(got))
	}
	if fact := findLayer(t, got[0], state.LayerHardware); !fact.Available || fact.Epoch != 1 {
		t.Fatalf("replayed hardware fact = %+v", fact)
	}
	var exported bytes.Buffer
	if err := store.ExportJSONLines(&exported); err != nil {
		t.Fatal(err)
	}
	fromExport, _ := NewReplay(time.Minute)
	if err := ReadJSONLines(&exported, fromExport, DefaultMaxRecordBytes); err != nil {
		t.Fatal(err)
	}
	exportedProjection := fromExport.Projections(now)
	if len(exportedProjection) != 1 {
		t.Fatalf("export projection count = %d", len(exportedProjection))
	}
	if fact := findLayer(t, exportedProjection[0], state.LayerHardware); !fact.Available || fact.Epoch != 1 {
		t.Fatalf("exported hardware fact = %+v", fact)
	}
}

func TestBoltStoreProtectsFileMode(t *testing.T) {
	store, path := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %o", info.Mode().Perm())
	}
}

func TestBoltStoreSerializesConcurrentWriters(t *testing.T) {
	store, _ := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	base := record(RoleAgent, state.LayerHardware, 1, true).Event
	base.EventID = "event-1"
	if _, err := store.Activate(base, now); err != nil {
		t.Fatal(err)
	}
	const writers = 24
	var wait sync.WaitGroup
	errorsSeen := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			event := base
			event.EventID = fmt.Sprintf("event-%d", index+2)
			event.Sequence = uint64(index + 2)
			_, err := store.Accept(event, now.Add(time.Duration(index+1)*time.Second))
			errorsSeen <- err
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	count, err := store.Count()
	if err != nil || count != writers+1 {
		t.Fatalf("count=%d error=%v", count, err)
	}
	replay, _ := NewReplay(time.Hour)
	if err := store.ReplayInto(replay); err != nil {
		t.Fatal(err)
	}
	fact := findLayer(t, replay.Projections(now.Add(time.Minute))[0], state.LayerHardware)
	if fact.Sequence != writers+1 {
		t.Fatalf("final sequence = %d", fact.Sequence)
	}
}

func TestSnapshotCheckpointRefreshesFactsWithoutAppendingUnchangedEvents(t *testing.T) {
	store, path := openTestStore(t)
	firstAt := time.Unix(1_800_100_000, 0).UTC()
	events := []Event{
		snapshotEvent(state.LayerVoWiFiRuntime, "runtime_ready", 1, firstAt),
		snapshotEvent(state.LayerTunnel, "tunnel_ready", 1, firstAt),
	}
	checkpoint := snapshotCheckpoint("generation-1", []state.Layer{state.LayerVoWiFiRuntime, state.LayerTunnel}, 1, firstAt, firstAt)
	records, stored, err := store.AcceptSnapshot(events, checkpoint)
	if err != nil || len(records) != 2 || !reflect.DeepEqual(stored, checkpoint) {
		t.Fatalf("first snapshot records=%d checkpoint=%+v err=%v", len(records), stored, err)
	}
	if count, _ := store.Count(); count != 2 {
		t.Fatalf("record count=%d", count)
	}

	secondAt := firstAt.Add(20 * time.Second)
	checkpoint.Sequence = 2
	checkpoint.ObservedAt = secondAt
	checkpoint.ReceivedAt = secondAt
	if records, _, err := store.AcceptSnapshot(nil, checkpoint); err != nil || len(records) != 0 {
		t.Fatalf("unchanged snapshot records=%d err=%v", len(records), err)
	}
	if count, _ := store.Count(); count != 2 {
		t.Fatalf("heartbeat appended records: %d", count)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	replay, _ := NewReplay(30 * time.Second)
	if err := store.ReplayInto(replay); err != nil {
		t.Fatal(err)
	}
	projection := replay.Projections(secondAt.Add(20 * time.Second))[0]
	fact := findLayer(t, projection, state.LayerTunnel)
	if !fact.Fresh || fact.ReceivedAt != secondAt {
		t.Fatalf("checkpoint did not refresh fact: %+v", fact)
	}

	older := checkpoint
	older.Sequence = 1
	older.ObservedAt = firstAt
	older.ReceivedAt = secondAt.Add(time.Second)
	if _, _, err := store.AcceptSnapshot(nil, older); !errors.Is(err, ErrOlderCheckpoint) {
		t.Fatalf("older checkpoint error=%v", err)
	}
}

func TestSnapshotCheckpointFencesReplacedGenerationAtomically(t *testing.T) {
	store, _ := openTestStore(t)
	now := time.Unix(1_800_200_000, 0).UTC()
	firstEvent := snapshotEvent(state.LayerTunnel, "tunnel_ready", 1, now)
	firstCheckpoint := snapshotCheckpoint("generation-1", []state.Layer{state.LayerTunnel}, 1, now, now)
	if _, _, err := store.AcceptSnapshot([]Event{firstEvent}, firstCheckpoint); err != nil {
		t.Fatal(err)
	}

	replacementEvent := firstEvent
	replacementEvent.EventID = "snapshot-generation-2-tunnel"
	replacementEvent.Generation = "generation-2"
	replacementEvent.Sequence = 1
	replacementEvent.Condition = state.ConditionBlocked
	replacementEvent.Available = false
	replacementEvent.Code = "tunnel_blocked"
	replacementCheckpoint := snapshotCheckpoint("generation-2", []state.Layer{state.LayerTunnel}, 1, now.Add(time.Second), now.Add(time.Second))
	records, _, err := store.AcceptSnapshot([]Event{replacementEvent}, replacementCheckpoint)
	if err != nil || len(records) != 1 || records[0].Epoch != 2 {
		t.Fatalf("replacement records=%+v err=%v", records, err)
	}

	late := firstCheckpoint
	late.Sequence = 99
	late.ObservedAt = now.Add(2 * time.Second)
	late.ReceivedAt = now.Add(2 * time.Second)
	if _, _, err := store.AcceptSnapshot(nil, late); !errors.Is(err, ErrUnauthorizedProducer) {
		t.Fatalf("late old checkpoint error=%v", err)
	}
	lateEvent := firstEvent
	lateEvent.EventID = "late-old-tunnel"
	lateEvent.Sequence = 99
	lateEvent.ObservedAt = late.ObservedAt
	if _, _, err := store.AcceptSnapshot([]Event{lateEvent}, late); !errors.Is(err, ErrGenerationReused) {
		t.Fatalf("reactivated old generation error=%v", err)
	}
	if count, _ := store.Count(); count != 2 {
		t.Fatalf("failed snapshot transaction changed records: %d", count)
	}
}

func snapshotEvent(layer state.Layer, code string, sequence uint64, at time.Time) Event {
	return Event{
		SchemaVersion: SchemaVersion, EventID: fmt.Sprintf("snapshot-generation-1-%d-%s", sequence, layer),
		LineID: "line-1", ProducerRole: RoleVoWiFi, ProducerID: "provider-1",
		Layer: layer, Condition: state.ConditionReady, Available: true, Code: code,
		Generation: "generation-1", Sequence: sequence, ObservedAt: at,
	}
}

func snapshotCheckpoint(generation string, layers []state.Layer, sequence uint64, observedAt, receivedAt time.Time) ProducerCheckpoint {
	return ProducerCheckpoint{
		LineID: "line-1", ProducerRole: RoleVoWiFi, ProducerID: "provider-1",
		Generation: generation, Layers: layers, Sequence: sequence, ObservedAt: observedAt, ReceivedAt: receivedAt,
	}
}

func TestBoltStoreRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		metadata, err := tx.CreateBucket(bucketMetadata)
		if err != nil {
			return err
		}
		return metadata.Put(keySchema, encodeUint64(BoltSchemaVersion+1))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBoltStore(path, time.Second); !errors.Is(err, ErrStoreSchema) {
		t.Fatalf("schema error = %v", err)
	}
}
