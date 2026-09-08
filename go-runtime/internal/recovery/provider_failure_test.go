package recovery

import (
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestProviderFailureRequiresCompleteUnansweredBootstrap(t *testing.T) {
	snapshot := vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-1", ProviderID: "provider-1",
		ProcessGeneration: "process-1", Sequence: 1, ObservedAt: time.Now().UTC(),
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeFailed, Code: "swu_open_failed",
			FailureID: strings.Repeat("a", 64), IKE: &vowifiipc.IKEExchangeEvidence{RequestsSent: 2, ResponseTimeouts: 2}},
		Tunnel: vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "swu_open_failed"},
		IMS:    vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped}, Voice: vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped},
		Messaging: vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped},
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := ClassifyProviderFailure(snapshot, 0, time.Minute); got != BlamesExit {
		t.Fatal(got)
	}
	if got := ClassifyProviderFailure(snapshot, time.Hour, time.Minute); got != BlamesElsewhere {
		t.Fatal("known healthy history was discarded", got)
	}
	for name, mutate := range map[string]func(*vowifiipc.Snapshot){
		"old provider":                func(s *vowifiipc.Snapshot) { s.Runtime.IKE = nil },
		"no sent request":             func(s *vowifiipc.Snapshot) { s.Runtime.IKE = &vowifiipc.IKEExchangeEvidence{} },
		"incomplete response wait":    func(s *vowifiipc.Snapshot) { s.Runtime.IKE.ResponseTimeouts = 1 },
		"peer replied before failure": func(s *vowifiipc.Snapshot) { s.Runtime.IKE.ResponseDatagrams, s.Runtime.IKE.ResponseTimeouts = 1, 1 },
		"SIM failure":                 func(s *vowifiipc.Snapshot) { s.Runtime.Code = "agent_aka_invalid" },
		"IMS rejection":               func(s *vowifiipc.Snapshot) { s.Runtime.Code = "ims_register_failed" },
		"cleanup failure":             func(s *vowifiipc.Snapshot) { s.Runtime.Code = "close_failed" },
		"unknown tunnel":              func(s *vowifiipc.Snapshot) { s.Tunnel.Condition = vowifiipc.LayerUnknown },
		"failure without identity":    func(s *vowifiipc.Snapshot) { s.Runtime.FailureID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			current := snapshot
			counts := *snapshot.Runtime.IKE
			current.Runtime.IKE = &counts
			mutate(&current)
			if got := ClassifyProviderFailure(current, 0, time.Minute); got != ExitUnclear {
				t.Fatal("insufficient evidence blamed shared exit", got)
			}
		})
	}
	ledger, failure := exitFixture(t)
	failure.Verdict = ClassifyProviderFailure(snapshot, 0, time.Minute)
	for i := 0; i < 3; i++ {
		id := strings.Repeat(string(rune('a'+i)), 64)
		var action ExitAction
		action, ledger = RecordExitFailureOnce(ledger, failure, id)
		if (i < 2 && action != ExitHold) || (i == 2 && action != ExitSwitch) {
			t.Fatal("old three-strike policy changed", action)
		}
		_, duplicate := RecordExitFailureOnce(ledger, failure, id)
		if duplicate.Failures != ledger.Failures {
			t.Fatal("one failure was counted twice")
		}
	}
}
