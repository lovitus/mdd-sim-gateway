package linecatalog

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validReceipt() OperationReceipt {
	now := time.Unix(1_800_000_000, 0).UTC()
	return OperationReceipt{SchemaVersion: OperationSchemaVersion, OperationID: "provision-01",
		Kind: OperationProvision, State: OperationPrepared, CreatedAt: now, UpdatedAt: now,
		RequestDigest: strings.Repeat("a", 64), AttemptCount: 1}
}

func TestOperationReceiptValidation(t *testing.T) {
	receipt := validReceipt()
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*OperationReceipt){
		"secret-like operation id": func(value *OperationReceipt) { value.OperationID = "pin secret" },
		"invalid digest":           func(value *OperationReceipt) { value.RequestDigest = "not-a-digest" },
		"zero attempts":            func(value *OperationReceipt) { value.AttemptCount = 0 },
		"backwards timestamp":      func(value *OperationReceipt) { value.UpdatedAt = value.CreatedAt.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := receipt
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("receipt unexpectedly accepted: %+v", candidate)
			}
		})
	}
}

func TestOperationReceiptPersistsInCatalogDatabaseAndRejectsOverwrite(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receipt := validReceipt()
	if err := store.PutOperation(receipt); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GetOperation(receipt.OperationID)
	if err != nil || !found || got != receipt {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
	receipt.State = OperationUnknown
	if err := store.PutOperation(receipt); err == nil {
		t.Fatal("overwriting an operation receipt was accepted")
	}
	missing, found, err := store.GetOperation("missing-operation")
	if err != nil || found || !missing.CreatedAt.IsZero() {
		t.Fatalf("missing=%+v found=%v err=%v", missing, found, err)
	}
}
