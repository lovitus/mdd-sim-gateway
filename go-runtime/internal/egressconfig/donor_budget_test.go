package egressconfig

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDonorBudgetSurvivesRestartAndCannotBeReissued(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress.db")
	card := "8944100000000000001"
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfigureDonor(card, 1, 8192, false); err != nil {
		t.Fatal(err)
	}
	grant := DonorGrant{SessionID: "one", LineID: "line-a", Purpose: "egress:hk", Target: json.RawMessage(`{"AgentID":"fixture"}`), MaxBytes: 16384}
	budget, err := store.ReserveDonor(card, grant)
	if err != nil || budget.Grant.MaxBytes != 8192 {
		t.Fatalf("grant=%+v %v", budget.Grant, err)
	}
	if err := store.RecordDonor(card, "one", 2048, false, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDonor(card, "one", 2048, false, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDonor(card, "one", 1024, false, false); err == nil {
		t.Fatal("decreasing counter accepted")
	}
	if _, err := store.ConfigureDonor(card, 2, 16384, true); err == nil {
		t.Fatal("reset refunded a possibly active owner")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	grant.SessionID, grant.Purpose, grant.LineID = "two", "egress:renamed", "line-b"
	if _, err := store.ReserveDonor(card, grant); err == nil {
		t.Fatal("restart/profile/line change reissued reserved allowance")
	}
	if err := store.RecordDonor(card, "one", 2048, true, true); err != nil {
		t.Fatal(err)
	}
	budget, err = store.DonorBudget(card)
	if err != nil || budget.UsedBytes != 2048 || budget.UncertainBytes != 6144 || budget.Grant != nil {
		t.Fatalf("lost evidence: %+v %v", budget, err)
	}
	if _, err := store.ReserveDonor(card, grant); err == nil {
		t.Fatal("unknown usage became free allowance")
	}
	if _, err := store.ConfigureDonor(card, 2, 8192, true); err != nil {
		t.Fatal(err)
	}
	budget, err = store.ReserveDonor(card, grant)
	if err != nil || budget.Grant.MaxBytes != 8192 {
		t.Fatalf("explicit reset failed: %+v %v", budget, err)
	}
}

func TestDonorResetAndAdmissionAreOneTransaction(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "egress.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	card := "8944100000000000001"
	if _, err := store.ConfigureDonor(card, 1, 4096, false); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var admitted, reset error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, admitted = store.ReserveDonor(card, DonorGrant{SessionID: "one", LineID: "line", Purpose: "manual", Target: json.RawMessage(`{}`), MaxBytes: 4096})
	}()
	go func() { defer wg.Done(); _, reset = store.ConfigureDonor(card, 2, 0, true) }()
	wg.Wait()
	if admitted == nil && reset == nil {
		t.Fatal("reset raced an authorized grant")
	}
	b, err := store.DonorBudget(card)
	if err != nil || b.Grant != nil && b.Grant.MaxBytes > b.LimitBytes {
		t.Fatalf("budget invariant failed: %+v %v", b, err)
	}
}
