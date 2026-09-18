package vowifiipc

import "testing"

func TestLivenessReviewPersistentIMSRequestsBudgetedRecovery(t *testing.T) {
	s := Snapshot{Runtime: RuntimeStatus{Condition: RuntimeRunning}, Tunnel: LayerStatus{Condition: LayerReady, Available: true}, IMS: LayerStatus{Condition: LayerBlocked, Code: "ims_recovery_failed"}}
	if !s.RequiresIdleRecovery() {
		t.Fatal("persistent IMS failure never reaches Core failure budget")
	}
}
