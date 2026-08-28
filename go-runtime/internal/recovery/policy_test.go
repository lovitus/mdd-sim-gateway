package recovery

import (
	"testing"
	"time"
)

func TestRecoverableFailureUsesCappedExponentialDelay(t *testing.T) {
	policy := Policy{Base: 30 * time.Second, Cap: time.Hour}
	want := []time.Duration{30, 60, 120, 240, 480, 960, 1920, 3600, 3600}
	for index, seconds := range want {
		decision, err := policy.Decide(Failure{Attempt: index + 1, Recoverable: true})
		if err != nil || !decision.Retry || decision.Action != ActionRetry || decision.After != seconds*time.Second {
			t.Fatalf("attempt %d = %+v, %v", index+1, decision, err)
		}
	}
}

func TestProviderDelayIsHonoredExactly(t *testing.T) {
	policy := Policy{Base: 30 * time.Second, Cap: time.Hour}
	delay := 1438 * time.Second
	decision, err := policy.Decide(Failure{Attempt: 9, Recoverable: true,
		ProviderDelay: &delay, Action: ActionReauthenticate})
	if err != nil || decision.After != delay || decision.Action != ActionReauthenticate {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
}

func TestPermanentFailureDoesNotInventRecovery(t *testing.T) {
	decision, err := (Policy{Base: time.Second, Cap: time.Minute}).Decide(Failure{Attempt: 1})
	if err != nil || decision.Retry || decision.Action != ActionNone {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
}

func TestRecoveryActionCannotSmuggleProcessRestart(t *testing.T) {
	_, err := (Policy{Base: time.Second, Cap: time.Minute}).Decide(Failure{
		Attempt: 1, Recoverable: true, Action: Action("restart_container"),
	})
	if err != ErrInvalidPolicy {
		t.Fatalf("error = %v, want ErrInvalidPolicy", err)
	}
}
