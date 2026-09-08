package runtimereconcile

import (
	"strings"
	"testing"
)

func TestPinnedRecoveryPauseRequiresNewManualRetryIdentity(t *testing.T) {
	r, catalog, line, observation := exitObserverFixture(t, true)
	for i := 0; i < 3; i++ {
		observation.status.Sequence = uint64(i + 1)
		observation.status.Runtime.FailureID = strings.Repeat(string(rune('a'+i)), 64)
		if err := r.observeExitRecovery(t.Context(), catalog, line, observation); err != nil {
			t.Fatal(err)
		}
	}
	if !r.exitPolicyPaused(line, observation) {
		t.Fatal("locked-node failure did not stop automatic recovery")
	}
	if err := r.resetExitPolicyForManualRetry(line.ID, "manual-one"); err != nil {
		t.Fatal(err)
	}
	if r.exitPolicyPaused(line, observation) {
		t.Fatal("explicit retry did not reopen recovery")
	}
	for i := 3; i < 6; i++ {
		observation.status.Sequence = uint64(i + 1)
		observation.status.Runtime.FailureID = strings.Repeat(string(rune('a'+i)), 64)
		if err := r.observeExitRecovery(t.Context(), catalog, line, observation); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.resetExitPolicyForManualRetry(line.ID, "manual-one"); err != nil {
		t.Fatal(err)
	}
	if !r.exitPolicyPaused(line, observation) {
		t.Fatal("replayed manual request reopened a later outage")
	}
	if err := r.resetExitPolicyForManualRetry(line.ID, "manual-two"); err != nil {
		t.Fatal(err)
	}
	if r.exitPolicyPaused(line, observation) {
		t.Fatal("new explicit retry was ignored")
	}
}
