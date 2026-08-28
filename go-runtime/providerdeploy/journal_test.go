package providerdeploy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

func TestJournalPersistsEveryStepAndArchivesOnlyTerminalReceipt(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "receipts")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := providerapply.Plan{SchemaVersion: 1, CatalogRevision: 2, Safe: true,
		Added: []providerapply.Change{}, Changed: []providerapply.Change{}, Removed: []providerapply.Change{}, Blockers: []providerapply.Blocker{}}
	journal, err := OpenJournal(directory, "/old", "/new", "apply-lease-1", plan)
	if err != nil {
		t.Fatal(err)
	}
	index, err := journal.Before("stop", "mdd-vowifi@line.service")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := os.ReadFile(filepath.Join(directory, "current.json"))
	var persisted Receipt
	if err := decodeReceipt(payload, &persisted); err != nil || persisted.Steps[0].State != StepPending {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	if err := journal.Complete(index); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(directory, "/old", "/next", "apply-lease-2", plan); !errors.Is(err, ErrIncompleteReceipt) {
		t.Fatalf("incomplete receipt error=%v", err)
	}
	if err := journal.Finish(StateApplied, "applied"); err != nil {
		t.Fatal(err)
	}
	next, err := OpenJournal(directory, "/new", "/next", "apply-lease-2", plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, journal.Receipt().ApplyID+".json")); err != nil {
		t.Fatal(err)
	}
	if next.Receipt().ApplyID == journal.Receipt().ApplyID {
		t.Fatal("new transaction reused prior apply ID")
	}
}

func TestJournalRefusesNonRegularCurrentReceipt(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "receipts")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(directory, "current.json")); err != nil {
		t.Fatal(err)
	}
	plan := providerapply.Plan{SchemaVersion: 1, CatalogRevision: 1, Safe: true}
	if _, err := OpenJournal(directory, "", "/new", "apply-lease-1", plan); !errors.Is(err, ErrIncompleteReceipt) {
		t.Fatalf("symlink current receipt error=%v", err)
	}
}

func TestJournalDoesNotArchiveTerminalReceiptWithPendingStep(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "receipts")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := providerapply.Plan{SchemaVersion: 1, CatalogRevision: 4, Safe: true}
	journal, err := OpenJournal(directory, "/old", "/new", "apply-lease-1", plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Before("switch_link", "/new"); err != nil {
		t.Fatal(err)
	}
	if err := journal.Finish(StateRolledBack, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(directory, "/old", "/next", "apply-lease-2", plan); !errors.Is(err, ErrIncompleteReceipt) {
		t.Fatalf("pending terminal receipt was archived: %v", err)
	}
}
