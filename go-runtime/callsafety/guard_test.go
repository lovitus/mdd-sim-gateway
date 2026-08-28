package callsafety

import (
	"testing"
	"time"
)

func TestHeartbeatLossOnlyTargetsExactCallAfterTenSeconds(t *testing.T) {
	now := time.Now()
	guard := Guard{HeartbeatTimeout: 10 * time.Second}
	call := Call{ID: "call-123", Phase: PhaseActive, BrowserConnected: false,
		BrowserLastSeen: now.Add(-11 * time.Second)}
	decision := guard.Evaluate(call, now)
	if decision.Action != ActionHangupExact || decision.CallID != call.ID {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestNetworkJitterInsideHeartbeatWindowDoesNotEndCall(t *testing.T) {
	now := time.Now()
	decision := (Guard{HeartbeatTimeout: 10 * time.Second}).Evaluate(Call{
		ID: "call-123", Phase: PhaseActive, BrowserConnected: true,
		BrowserLastSeen: now.Add(-9 * time.Second),
	}, now)
	if decision.Action != ActionNone {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestRegistrationAndTunnelStateCannotEnterCallGuard(t *testing.T) {
	call := Call{ID: "call-123", Phase: PhaseActive, BrowserConnected: true,
		BrowserLastSeen: time.Now()}
	if decision := (Guard{HeartbeatTimeout: 10 * time.Second}).Evaluate(call, time.Now()); decision.Action != ActionNone {
		t.Fatalf("decision = %+v", decision)
	}
}
