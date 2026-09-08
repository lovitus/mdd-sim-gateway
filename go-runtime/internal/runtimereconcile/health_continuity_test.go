package runtimereconcile

import (
	"errors"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestHealthWindowRequiresIMSAndTunnelOnSameProvider(t *testing.T) {
	reconciler, _, runtime, _, _, clock := testReconciler(t, vowifiipc.RuntimeRunning, oneCard())
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	observation := lineObservation{fence: runtime.fence, status: runtime.status}
	observation.status.Tunnel, observation.status.IMS = ready, ready
	state := reconciler.lineLocked("line-1")
	state.recoveryFailures, state.recoveryNext = 3, clock.Now().Add(time.Hour)
	reconciler.observeHealthy("line-1", observation)
	clock.Advance(30 * time.Second)
	observation.status.IMS = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "ims_not_registered"}
	reconciler.observeHealthy("line-1", observation)
	clock.Advance(2 * time.Minute)
	reconciler.observeHealthy("line-1", observation)
	if !state.healthySince.IsZero() || state.recoveryFailures != 3 || state.recoveryNext.IsZero() {
		t.Fatal("running without IMS erased the recovery budget")
	}
	observation.status.IMS = ready
	reconciler.observeHealthy("line-1", observation)
	clock.Advance(40 * time.Second)
	observation.fence.Generation = "replacement-generation"
	reconciler.observeHealthy("line-1", observation)
	clock.Advance(30 * time.Second)
	reconciler.observeHealthy("line-1", observation)
	if state.recoveryFailures != 3 {
		t.Fatal("healthy time crossed Provider generations")
	}
	clock.Advance(30 * time.Second)
	reconciler.observeHealthy("line-1", observation)
	if state.recoveryFailures != 0 || !state.recoveryNext.IsZero() {
		t.Fatal("confirmed stable IMS did not clear the recovery budget")
	}
}

func TestUnconfirmedLayerCannotAccumulateHealthyTime(t *testing.T) {
	for _, layer := range []string{"ims", "tunnel"} {
		t.Run(layer, func(t *testing.T) {
			reconciler, _, runtime, _, _, clock := testReconciler(t, vowifiipc.RuntimeRunning, oneCard())
			ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
			observation := lineObservation{fence: runtime.fence, status: runtime.status}
			observation.status.Tunnel, observation.status.IMS = ready, ready
			state := reconciler.lineLocked("line-1")
			state.recoveryFailures = 2
			reconciler.observeHealthy("line-1", observation)
			if layer == "ims" {
				observation.status.IMS.Available = false
			} else {
				observation.status.Tunnel.Available = false
			}
			clock.Advance(2 * time.Minute)
			reconciler.observeHealthy("line-1", observation)
			if !state.healthySince.IsZero() || state.recoveryFailures != 2 {
				t.Fatal("unavailable layer was treated as healthy")
			}
		})
	}
}

func TestObservationFailureBreaksHealthWindowWithoutLifecycleAction(t *testing.T) {
	reconciler, catalog, runtime, _, _, clock := testReconciler(t, vowifiipc.RuntimeRunning, oneCard())
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
		t.Fatal(err)
	}
	state := reconciler.lineLocked("line-1")
	state.healthySince, state.healthyFence = clock.Now(), runtime.fence
	state.recoveryFailures = 3
	runtime.mu.Lock()
	runtime.observeErr = errors.New("Provider snapshot unavailable")
	runtime.mu.Unlock()
	if err := reconciler.reconcile(t.Context()); err == nil {
		t.Fatal("observation error was hidden")
	}
	if !state.healthySince.IsZero() || state.recoveryFailures != 3 {
		t.Fatal("unknown interval was included in healthy time")
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("observation failure triggered %s", action)
	default:
	}
}

func TestPendingIncomingCallDoesNotConsumeRecoveryEpisode(t *testing.T) {
	for _, condition := range []vowifiipc.RuntimeCondition{vowifiipc.RuntimeRunning, vowifiipc.RuntimeFailed} {
		t.Run(string(condition), func(t *testing.T) {
			reconciler, catalog, runtime, _, _, clock := testReconciler(t, condition, oneCard())
			if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
				t.Fatal(err)
			}
			runtime.mu.Lock()
			runtime.status.Tunnel = vowifiipc.LayerStatus{Condition: vowifiipc.LayerDegraded, Code: "userspace_stack_failed"}
			runtime.status.PendingIncomingCall = &vowifiipc.PendingIncomingCall{
				CallID: "pending-call", Caller: "fixture-peer", Callee: "fixture-line", ReceivedAt: clock.Now(),
			}
			runtime.mu.Unlock()
			if err := reconciler.reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			reconciler.mu.Lock()
			state := reconciler.lineLocked("line-1")
			recovering, failures := state.recovering, state.recoveryFailures
			reconciler.mu.Unlock()
			if recovering || failures != 0 {
				t.Fatal("ringing call consumed a recovery episode")
			}
			select {
			case action := <-runtime.actions:
				t.Fatalf("ringing call triggered %s", action)
			default:
			}
			line, err := catalog.Get("line-1")
			if err != nil {
				t.Fatal(err)
			}
			intent, found, _, epoch, err := reconciler.readIntent(line.ID)
			if err != nil {
				t.Fatal(err)
			}
			reconciler.mu.Lock()
			state.recovering, state.recoveryEpisode = true, 1
			reconciler.mu.Unlock()
			plan := reconciler.actionPlan(line, lineObservation{intentEnabled: intent, intentFound: found,
				intentEpoch: epoch, cardMatches: 1, providerReady: true, fence: runtime.fence}, "stop", true)
			if err := reconciler.validatePlan(t.Context(), plan); !errors.Is(err, errActionPlanChanged) {
				t.Fatalf("call arriving after planning was not fenced: %v", err)
			}
		})
	}
}

func TestUnregisteredRuntimeKeepsProviderRegistrationOwnership(t *testing.T) {
	reconciler, catalog, runtime, _, _, clock := testReconciler(t, vowifiipc.RuntimeRunning, oneCard())
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.status.Tunnel = vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	runtime.status.IMS = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "ims_not_registered"}
	runtime.mu.Unlock()
	state := reconciler.lineLocked("line-1")
	state.healthySince, state.healthyFence = clock.Now().Add(-2*time.Minute), runtime.fence
	state.recoveryFailures, state.recoveryNext = 3, clock.Now().Add(time.Minute)
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !state.healthySince.IsZero() || state.recoveryFailures != 3 || state.recoveryNext.IsZero() {
		t.Fatal("unregistered IMS was recorded as stable recovery")
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("ordinary IMS registration caused Core to %s", action)
	default:
	}
}
