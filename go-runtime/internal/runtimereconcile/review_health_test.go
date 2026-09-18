package runtimereconcile

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"testing"
	"time"
)

func TestLivenessReviewRecoveryPreservesAdmissionGuards(t *testing.T) {
	for _, guard := range []string{"carrier", "registering", "call", "incoming", "maintenance"} {
		t.Run(guard, func(t *testing.T) {
			r, catalog, runtime, _, _, clock := testReconciler(t, vowifiipc.RuntimeRunning, oneCard())
			if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
				t.Fatal(err)
			}
			runtime.status.Tunnel = vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true}
			runtime.status.IMS = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "ims_recovery_failed"}
			future := clock.Now().Add(time.Hour)
			switch guard {
			case "carrier":
				runtime.status.Runtime.Health = &vowifiipc.RuntimeHealth{IMSRetryAfterUntil: &future}
			case "registering":
				runtime.status.Runtime.Health = &vowifiipc.RuntimeHealth{IMSRecovering: true}
			case "call":
				runtime.status.ActiveCall = &vowifiipc.ActiveCall{CallID: "call"}
			case "incoming":
				runtime.status.PendingIncomingCall = &vowifiipc.PendingIncomingCall{CallID: "incoming"}
			case "maintenance":
				runtime.status.Maintenance.Draining = true
			}
			if err := r.reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case action := <-runtime.actions:
				t.Fatalf("guard %s allowed recovery %s", guard, action)
			default:
			}
			r.mu.Lock()
			state := r.lines["line-1"]
			started := state != nil && state.inFlight
			r.mu.Unlock()
			if started {
				t.Fatal("guarded recovery started asynchronous operation")
			}
		})
	}
}
