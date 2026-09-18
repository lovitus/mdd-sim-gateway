package swu

import (
	"testing"
	"time"
)

func TestLivenessReviewCountsEachFailedProbeOnce(t *testing.T) {
	now := time.Unix(1800000000, 0)
	state, err := NewIKELivenessState(IKELivenessConfig{DisableKeepalive: true, DPDInterval: time.Second, DPDTimeout: time.Second, MaxMissedDPDProbes: 3}, now)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		now = now.Add(time.Second)
		decision := state.Advance(now)
		if decision.Action != IKELivenessSendDPD {
			t.Fatalf("probe %d not sent; budget charged twice: %+v", attempt, decision)
		}
		state.RecordLivenessResult(now, false)
		snapshot := state.Snapshot()
		if snapshot.MissedDPDProbes != attempt || snapshot.Dead != (attempt == 3) {
			t.Fatalf("budget charged twice: attempt=%d snapshot=%+v", attempt, snapshot)
		}
	}
}
