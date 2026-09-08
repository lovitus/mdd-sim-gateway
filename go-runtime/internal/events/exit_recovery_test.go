package events

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

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
