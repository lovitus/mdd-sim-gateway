package events

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

func TestPendingExitSelectionCannotBePurged(t *testing.T) {
	store, err := OpenBoltStore(filepath.Join(t.TempDir(), "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := egressconfig.RecoveryRequest{SchemaVersion: egressconfig.SchemaVersion, ConfigRevision: 1, CatalogRevision: 1,
		LineID: "line-1", ProviderGeneration: "provider-1", FailureID: strings.Repeat("a", 64), ExpectedGeneration: strings.Repeat("b", 64),
		Country: "gb", FromNode: "node-a", ToNode: "node-b"}
	ledger := recovery.ExitLedger{Selection: &recovery.ExitSelection{Request: request, State: "unknown", Attempts: 1}}
	stored, err := store.PutExitRecoveryExpected("line-1", ledger, 0)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := NewReplay(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	purger, err := NewLinePurger(store, replay)
	if err != nil {
		t.Fatal(err)
	}
	if active, err := store.ActiveExitRecovery("line-1"); err != nil || !active {
		t.Fatal(active, err)
	}
	if err := purger.PurgeLine("line-1"); !errors.Is(err, ErrExitRecoveryPending) {
		t.Fatal("unresolved recovery material was deleted", err)
	}
	retained, err := store.ExitRecovery("line-1")
	if err != nil || retained.Revision != stored.Revision {
		t.Fatal(retained, err)
	}
	ledger.Selection.State = "applied"
	ledger.Selection.Code = "runtime_confirmed"
	if _, err := store.PutExitRecoveryExpected("line-1", ledger, stored.Revision); err != nil {
		t.Fatal(err)
	}
	if err := purger.PurgeLine("line-1"); err != nil {
		t.Fatal(err)
	}
	if active, err := store.ActiveExitRecovery("line-1"); err != nil || active {
		t.Fatal(active, err)
	}
}

func TestExitRecoverySurvivesReopenAndRejectsStaleWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	store, err := OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := store.ExitRecovery("line-1")
	if err != nil || empty.Revision != 0 {
		t.Fatal(empty, err)
	}
	ledger := recovery.ExitLedger{CampaignEpoch: "campaign", SampleGeneration: "generation", StableCardKey: "card", LineConfigEpoch: "config", LastFailureID: "failure-1", Failures: 1, Strikes: 1, Tried: []string{"node-a"}}
	first, err := store.PutExitRecoveryExpected("line-1", ledger, 0)
	if err != nil || first.Revision != 1 {
		t.Fatal(first, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.ExitRecovery("line-1")
	if err != nil || restored.Ledger.LastFailureID != "failure-1" || restored.Revision != 1 {
		t.Fatal(restored, err)
	}
	_, replayed := recovery.RecordExitFailureOnce(restored.Ledger, recovery.ExitFailure{Verdict: recovery.BlamesExit, Node: "node-a", CampaignEpoch: "campaign", SampleGeneration: "generation", ExpectedSampleGeneration: "generation"}, "failure-1")
	if replayed.Failures != 1 || replayed.Strikes != 1 {
		t.Fatal("restart recounted failure", replayed)
	}
	duplicate, err := store.PutExitRecoveryExpected("line-1", restored.Ledger, 1)
	if err != nil || duplicate.Revision != 1 {
		t.Fatal("duplicate changed revision", duplicate, err)
	}
	ledger.Failures = 2
	if _, err := store.PutExitRecoveryExpected("line-1", ledger, 0); !errors.Is(err, ErrExitRecoveryRevision) {
		t.Fatal("stale write accepted", err)
	}
	unchanged, _ := store.ExitRecovery("line-1")
	if unchanged.Ledger.Failures != 1 {
		t.Fatal("stale write replaced ledger")
	}
	restored.Ledger.Tried[0] = "edited"
	independent, _ := store.ExitRecovery("line-1")
	if independent.Ledger.Tried[0] != "node-a" {
		t.Fatal("read aliases persisted state")
	}
	replay, _ := NewReplay(time.Minute)
	purger, _ := NewLinePurger(store, replay)
	if err := purger.PurgeLine("line-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExitRecovery("line-1"); !errors.Is(err, ErrExitRecoveryDeleted) {
		t.Fatal(err)
	}
	if _, err := store.PutExitRecoveryExpected("line-1", ledger, 0); !errors.Is(err, ErrExitRecoveryDeleted) {
		t.Fatal("purged recovery resurrected", err)
	}
}
