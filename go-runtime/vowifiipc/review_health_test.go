package vowifiipc

import (
	"testing"
	"time"
)

func TestLivenessReviewOnlyCarrierDeadlineBlocksOuterRecovery(t *testing.T) {
	future := time.Now().Add(time.Hour)
	s := Snapshot{Runtime: RuntimeStatus{Health: &RuntimeHealth{IMSNextAttemptAt: &future}}}
	if s.IdleRecoveryBlockedAt(time.Now()) {
		t.Fatal("routine IMS retry starves Core failure budget")
	}
	s.Runtime.Health.IMSRetryAfterUntil = &future
	if !s.IdleRecoveryBlockedAt(time.Now()) {
		t.Fatal("carrier Retry-After ignored")
	}
	if s.IdleRecoveryBlockedAt(future) {
		t.Fatal("expired holdoff kept blocking")
	}
	s.Runtime.Health.IMSRecovering = true
	if !s.IdleRecoveryBlockedAt(future) {
		t.Fatal("active registration owner ignored")
	}
	if err := s.Runtime.Health.Validate(); err != nil {
		t.Fatal(err)
	}
}
