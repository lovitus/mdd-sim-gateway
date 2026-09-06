package linecatalog

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestDeletionLedgerReleasesCatalogIdentityOnlyAfterAllStages(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := testLine("line-delete", "8944100000000000099")
	line.Enabled = false
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetDeletedExpected(line.ID, true, 2); err != nil {
		t.Fatal(err)
	}
	err = store.db.Update(func(tx *bolt.Tx) error {
		binding := RawModemBinding{SchemaVersion: 1, Epoch: 2, LineID: line.ID, SourceAgentID: "source-agent",
			EquipmentID: "123456789012345", CardID: line.CardID, ImporterAgentID: "importer-agent", Enabled: false}
		wire, _ := json.Marshal(binding)
		return tx.Bucket(rawModemBindingsBucket).Put([]byte(line.ID), wire)
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, created, err := store.PrepareDeletionExpected(line.ID, "delete-operation-1", true, 3, time.Now())
	if err != nil || !created || receipt.Stage != DeletionPrepared {
		t.Fatalf("prepare=%+v created=%v err=%v", receipt, created, err)
	}
	if _, err := store.Get(line.ID); err != nil {
		t.Fatalf("prepared deletion removed line early: %v", err)
	}
	if _, _, err := store.SetDeletedExpected(line.ID, false, 3); !errors.Is(err, ErrLineOperationActive) {
		t.Fatalf("active deletion allowed restore: %v", err)
	}
	line.Name = "mutated"
	if _, _, err := store.PutExpectedManaged(line, 3); !errors.Is(err, ErrLineOperationActive) {
		t.Fatalf("active deletion allowed catalog update: %v", err)
	}
	stages := []DeletionStage{DeletionNotifications, DeletionEvents, DeletionAllowance, DeletionMessages, DeletionSMSOperations, DeletionCalls}
	prior := DeletionPrepared
	for _, next := range stages {
		receipt, err = store.AdvanceDeletion(receipt.OperationID, prior, next, time.Now())
		if err != nil || receipt.Stage != next {
			t.Fatalf("advance %s: %+v err=%v", next, receipt, err)
		}
		prior = next
	}
	receipt, err = store.FinalizeDeletion(receipt.OperationID, time.Now())
	if err != nil || receipt.Stage != DeletionSucceeded || receipt.CatalogRevision != 4 {
		t.Fatalf("finalize=%+v err=%v", receipt, err)
	}
	if _, err := store.Get(line.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted line remains: %v", err)
	}
	if _, _, err := store.CreateExpected(testLine("replacement", line.CardID), 4); err != nil {
		t.Fatalf("card identity was not released: %v", err)
	}
	if _, _, err := store.CreateExpected(testLine(line.ID, "8944100000000000077"), 5); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("permanently deleted line ID was reusable: %v", err)
	}
	retry, created, err := store.PrepareDeletionExpected(line.ID, receipt.OperationID, true, 3, time.Now())
	if err != nil || created || retry.Stage != DeletionSucceeded {
		t.Fatalf("idempotent retry=%+v created=%v err=%v", retry, created, err)
	}
}

func TestDeletionPrepareRejectsActiveRawBinding(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := testLine("line-raw", "8944100000000000088")
	line.Enabled = false
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetDeletedExpected(line.ID, true, 2); err != nil {
		t.Fatal(err)
	}
	err = store.db.Update(func(tx *bolt.Tx) error {
		binding := RawModemBinding{SchemaVersion: 1, Epoch: 2, LineID: line.ID, SourceAgentID: "source-agent",
			EquipmentID: "123456789012345", CardID: line.CardID, ImporterAgentID: "importer-agent", Enabled: true}
		wire, _ := json.Marshal(binding)
		return tx.Bucket(rawModemBindingsBucket).Put([]byte(line.ID), wire)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PrepareDeletionExpected(line.ID, "delete-operation-raw", true, 3, time.Now()); !errors.Is(err, ErrLineActive) {
		t.Fatalf("active raw binding error=%v", err)
	}
}
