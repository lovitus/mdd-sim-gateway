package rawcapture

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestExplicitAdaptedSelectionSurvivesReopenWithoutRawCapture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pair := Pair{EquipmentID: "867530900000001", CardID: "8944100000000000001"}
	if err := store.SetAdapted(pair, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot()
	if err != nil || len(snapshot.Desired) != 0 || len(snapshot.Captures) != 0 || len(snapshot.ModeSelections) != 1 ||
		snapshot.ModeSelections[0] != (ModeSelection{Pair: pair, Mode: "adapted"}) {
		t.Fatal("explicit adapted mode confused with no selection", err)
	}
	if err := store.SetRaw(pair); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot()
	if err != nil || len(snapshot.Desired) != 1 || snapshot.ModeSelections[0].Mode != "raw" {
		t.Fatal("raw intent and saved mode diverged", err)
	}
	other := Pair{EquipmentID: pair.EquipmentID, CardID: "8944100000000000002"}
	if err := store.SetAdapted(other, time.Now()); !errors.Is(err, ErrModeChanged) {
		t.Fatal("foreign card cleared mode", err)
	}
	snapshot, _ = store.Snapshot()
	if snapshot.ModeSelections[0].Pair != pair || snapshot.ModeSelections[0].Mode != "raw" || len(snapshot.Desired) != 1 {
		t.Fatal("failed mutation changed durable choice")
	}
}

func TestModeSelectionAbsenceDoesNotInventAnAdaptedChoice(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "capture.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot()
	if err != nil || len(snapshot.ModeSelections) != 0 {
		t.Fatal("missing historical choice guessed", err)
	}
	pair := Pair{EquipmentID: "867530900000001", CardID: "8944100000000000001"}
	if err := store.setAdapted(pair, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot()
	if err != nil || len(snapshot.ModeSelections) != 0 {
		t.Fatal("housekeeping invented a user selection", err)
	}
}
