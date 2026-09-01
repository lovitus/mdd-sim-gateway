package linecatalog

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRawModemBindingUsesIndependentRevisionAndExactLineIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := testLine("line-raw", "8944100000000000001")
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	catalogBefore, _ := store.Snapshot()
	actualEquipment := "867530900000099"
	binding := RawModemBinding{
		LineID: line.ID, SourceAgentID: "windows-agent-a", EquipmentID: actualEquipment,
		CardID: line.CardID, ImporterAgentID: "server-agent", Enabled: true,
	}
	got, revision, changed, err := store.PutRawModemBindingExpected(binding, 1)
	if err != nil || !changed || revision != 2 || got.SchemaVersion != RawModemBindingSchemaVersion || got.Epoch != 2 {
		t.Fatalf("binding=%+v revision=%d changed=%t err=%v", got, revision, changed, err)
	}
	catalogAfter, _ := store.Snapshot()
	if catalogAfter.Revision != catalogBefore.Revision {
		t.Fatalf("raw binding advanced catalog revision %d -> %d", catalogBefore.Revision, catalogAfter.Revision)
	}
	snapshot, err := store.RawModemBindings()
	if err != nil || snapshot.Revision != 2 || len(snapshot.Bindings) != 1 || snapshot.Bindings[0] != got {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if _, revision, changed, err := store.PutRawModemBindingExpected(got, 2); err != nil || changed || revision != 2 {
		t.Fatalf("idempotent revision=%d changed=%t err=%v", revision, changed, err)
	}
	changedCard := got
	changedCard.CardID = "8944100000000000002"
	if _, _, _, err := store.PutRawModemBindingExpected(changedCard, 2); err == nil {
		t.Fatal("raw binding accepted a card different from the line")
	}
}

func TestRawModemBindingEpochChangesOnlyForThatBinding(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testLine("line-a", "8944100000000000001")
	second := testLine("line-b", "8944100000000000002")
	if _, err := store.Put(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(second); err != nil {
		t.Fatal(err)
	}
	firstBinding, revision, _, err := store.PutRawModemBindingExpected(RawModemBinding{
		LineID: first.ID, SourceAgentID: "windows-agent-a", EquipmentID: first.SIM.IMEI,
		CardID: first.CardID, ImporterAgentID: "server-agent", Enabled: true,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.PutRawModemBindingExpected(RawModemBinding{
		LineID: second.ID, SourceAgentID: "windows-agent-b", EquipmentID: second.SIM.IMEI,
		CardID: second.CardID, ImporterAgentID: "server-agent", Enabled: true,
	}, revision); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.RawModemBindings()
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range snapshot.Bindings {
		if binding.LineID == first.ID && binding.Epoch != firstBinding.Epoch {
			t.Fatalf("unrelated binding changed epoch %d -> %d", firstBinding.Epoch, binding.Epoch)
		}
	}
}

func TestRawModemBindingCASAndSourceUniqueness(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testLine("line-a", "8944100000000000001")
	second := testLine("line-b", "8944100000000000002")
	second.SIM.IMEI = first.SIM.IMEI
	if _, err := store.Put(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(second); err != nil {
		t.Fatal(err)
	}
	binding := RawModemBinding{
		LineID: first.ID, SourceAgentID: "windows-agent-a", EquipmentID: first.SIM.IMEI,
		CardID: first.CardID, ImporterAgentID: "server-agent", Enabled: true,
	}
	if _, _, _, err := store.PutRawModemBindingExpected(binding, 9); !errors.Is(err, ErrRawModemRevision) {
		t.Fatalf("stale CAS err=%v", err)
	}
	if _, _, _, err := store.PutRawModemBindingExpected(binding, 1); err != nil {
		t.Fatal(err)
	}
	secondBinding := binding
	secondBinding.LineID, secondBinding.CardID = second.ID, second.CardID
	if _, _, _, err := store.PutRawModemBindingExpected(secondBinding, 2); err != nil {
		// The ICCID is part of the source triple, so a different inserted card
		// is a different binding; line-level card uniqueness remains intact.
		t.Fatal(err)
	}
}

func TestRawModemBindingCanDisableExactStoredIdentityAfterLineReplacement(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := testLine("line-raw", "8944100000000000001")
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	binding, revision, _, err := store.PutRawModemBindingExpected(RawModemBinding{
		LineID: line.ID, SourceAgentID: "windows-agent-a", EquipmentID: line.SIM.IMEI,
		CardID: line.CardID, ImporterAgentID: "server-agent", Enabled: true,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	line.CardID, line.SIM.IMEI = "8944100000000000002", "867530900000002"
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	binding.Enabled = false
	disabled, nextRevision, changed, err := store.PutRawModemBindingExpected(binding, revision)
	if err != nil || !changed || disabled.Enabled || nextRevision != revision+1 ||
		disabled.EquipmentID != binding.EquipmentID || disabled.CardID != binding.CardID {
		t.Fatalf("binding=%+v revision=%d changed=%t err=%v", disabled, nextRevision, changed, err)
	}
}
