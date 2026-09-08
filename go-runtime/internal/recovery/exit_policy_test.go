package recovery

import (
	"reflect"
	"testing"
	"time"
)

func exitFixture(t *testing.T) (ExitLedger, ExitFailure) {
	t.Helper()
	ledger, err := BeginExitCampaign(ExitLedger{}, ExitCampaign{Epoch: "campaign", SampleGeneration: "generation", StableCardKey: "card", LineConfigEpoch: "config"})
	if err != nil {
		t.Fatal(err)
	}
	return ledger, ExitFailure{Verdict: BlamesExit, Node: "node-a", Candidates: []string{"node-a", "node-b"}, CampaignEpoch: "campaign", SampleGeneration: "generation", ExpectedSampleGeneration: "generation"}
}

func TestExitRecoveryThreeStrikesAndExhaustedCadence(t *testing.T) {
	ledger, failure := exitFixture(t)
	for i := 0; i < 2; i++ {
		action, next := RecordExitFailure(ledger, failure)
		if action != ExitHold {
			t.Fatal(action)
		}
		ledger = next
	}
	action, ledger := RecordExitFailure(ledger, failure)
	if action != ExitSwitch || !reflect.DeepEqual(ledger.Tried, []string{"node-a"}) {
		t.Fatalf("%s %+v", action, ledger)
	}
	failure.Node = "node-b"
	for i := 0; i < 3; i++ {
		action, ledger = RecordExitFailure(ledger, failure)
	}
	if action != ExitBackOff || !ledger.Exhausted || ExhaustedRetry != time.Hour {
		t.Fatalf("%s %+v", action, ledger)
	}
	action, ledger = RecordExitFailure(ledger, failure)
	if action != ExitBackOff {
		t.Fatal(action)
	}
	failure.Verdict = BlamesElsewhere
	_, ledger = RecordExitFailure(ledger, failure)
	if ledger.Exhausted || len(ledger.Tried) != 0 || ledger.Strikes != 0 {
		t.Fatal(ledger)
	}
}

func TestExitRecoveryCountsStableFailureOnlyOnce(t *testing.T) {
	ledger, failure := exitFixture(t)
	_, ledger = RecordExitFailureOnce(ledger, failure, "failure-one")
	if ledger.Failures != 1 || ledger.Strikes != 1 {
		t.Fatal(ledger)
	}
	for i := 0; i < 20; i++ {
		action, next := RecordExitFailureOnce(ledger, failure, "failure-one")
		if action != ExitHold || !reflect.DeepEqual(next, ledger) {
			t.Fatal("poll counted as another failure", next)
		}
	}
	_, ledger = RecordExitFailureOnce(ledger, failure, "failure-two")
	action, ledger := RecordExitFailureOnce(ledger, failure, "failure-three")
	if action != ExitSwitch || ledger.Failures != 3 {
		t.Fatal(action, ledger)
	}
}

func TestExitRecoveryUnknownEvidenceDoesNotConsumeIdentity(t *testing.T) {
	ledger, failure := exitFixture(t)
	unknown := failure
	unknown.Verdict = ExitUnclear
	_, next := RecordExitFailureOnce(ledger, unknown, "failure-one")
	if next.Failures != 0 || next.LastFailureID != "" {
		t.Fatal(next)
	}
	_, next = RecordExitFailureOnce(next, failure, "failure-one")
	if next.Failures != 1 {
		t.Fatal("later evidence was suppressed", next)
	}
	_, missing := RecordExitFailureOnce(next, failure, "")
	if !reflect.DeepEqual(missing, next) {
		t.Fatal("missing identity counted")
	}
}

func TestExitRecoveryHealthyPeerNeverEvictedAndReportOnce(t *testing.T) {
	ledger, failure := exitFixture(t)
	failure.PeerRegistered = true
	for i := 1; i <= 7; i++ {
		action, next := RecordExitFailure(ledger, failure)
		ledger = next
		want := ExitHold
		if i == 6 {
			want = ExitReport
		}
		if i == 7 {
			want = ExitPace
		}
		if action != want || ledger.Strikes != 0 || len(ledger.Tried) != 0 || !ledger.HeldForPeer {
			t.Fatalf("sample %d: %s %+v", i, action, ledger)
		}
	}
}

func TestExitRecoveryPinnedGiveUpOnceAndUnpinResumes(t *testing.T) {
	ledger, failure := exitFixture(t)
	failure.Pinned = true
	var action ExitAction
	for i := 0; i < 3; i++ {
		action, ledger = RecordExitFailure(ledger, failure)
	}
	if action != ExitGiveUp || !ledger.GivenUp {
		t.Fatal(action, ledger)
	}
	action, ledger = RecordExitFailure(ledger, failure)
	if action != ExitHold {
		t.Fatal(action)
	}
	failure.Pinned = false
	action, ledger = RecordExitFailure(ledger, failure)
	if action != ExitSwitch || ledger.GivenUp {
		t.Fatal(action, ledger)
	}
}

func TestExitRecoveryUnknownOrStaleEvidenceDoesNotAdvance(t *testing.T) {
	ledger, failure := exitFixture(t)
	ledger.Failures = 6
	ledger.Reported = true
	for _, mutate := range []func(*ExitFailure){func(f *ExitFailure) { f.Verdict = ExitUnclear }, func(f *ExitFailure) { f.CampaignEpoch = "other" }, func(f *ExitFailure) { f.ExpectedSampleGeneration = "" }, func(f *ExitFailure) { f.SampleGeneration = "other" }} {
		copy := failure
		mutate(&copy)
		action, next := RecordExitFailure(ledger, copy)
		if action != ExitHold || !reflect.DeepEqual(next, ledger) {
			t.Fatal(action, next)
		}
	}
	connected := true
	disconnected := false
	zero := 0
	many := 6
	for _, test := range []struct {
		connected *bool
		counter   *int
		want      ExitVerdict
	}{{nil, nil, ExitUnclear}, {&connected, nil, ExitUnclear}, {&connected, &zero, ExitUnclear}, {&connected, &many, ExitUnclear}, {&disconnected, nil, ExitUnclear}, {&disconnected, &zero, BlamesExit}} {
		if got := ClassifyExit(test.connected, test.counter, 0, 0); got != test.want {
			t.Fatal(got, test.want)
		}
	}
	if ClassifyExit(&disconnected, &zero, time.Hour, time.Minute) != BlamesElsewhere {
		t.Fatal("stable evidence lost")
	}
	if ClassifyExit(&disconnected, nil, time.Hour, time.Minute) != ExitUnclear {
		t.Fatal("historical stability replaced missing current evidence")
	}
	if ClassifyExit(&connected, &zero, time.Second, time.Hour) != BlamesElsewhere {
		t.Fatal("known healthy stretch was ignored")
	}
}

func TestExitRecoveryCampaignIdentityAndCopyIsolation(t *testing.T) {
	ledger, failure := exitFixture(t)
	ledger.Tried = []string{"old"}
	next, err := BeginExitCampaign(ledger, ExitCampaign{Epoch: "campaign", SampleGeneration: "unexpected", StableCardKey: "card", LineConfigEpoch: "config"})
	if err != nil || !reflect.DeepEqual(next, ledger) {
		t.Fatal(next, err)
	}
	next, err = BeginExitCampaign(ledger, ExitCampaign{Epoch: "campaign", SampleGeneration: "new", StableCardKey: "card", LineConfigEpoch: "config", ControlledRebuild: true})
	if err != nil || next.SampleGeneration != "new" || len(next.Tried) != 1 {
		t.Fatal(next, err)
	}
	_, next = RecordExitFailure(ledger, failure)
	next.Tried[0] = "changed"
	if ledger.Tried[0] != "old" {
		t.Fatal("policy mutated caller ledger")
	}
}
