package runtimehost

import (
	"errors"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"testing"
	"time"
)

// No clock sleeps or carrier are needed to cross the registration expiry.

func TestLivenessReviewManualReportsExistingOwnerAndBackoff(t *testing.T) {
	m := &imsRegistrationMaintenance{}
	m.operationMu.Lock()
	_, err := m.Recover(t.Context())
	m.operationMu.Unlock()
	if !errors.Is(err, ErrIMSRegistrationInProgress) {
		t.Fatalf("busy recovery=%v", err)
	}
	m.recoveryState.NextAttemptAt = time.Now().Add(time.Hour)
	_, err = m.Recover(t.Context())
	if !errors.Is(err, ErrIMSRegistrationRetryPending) {
		t.Fatalf("pending recovery=%v", err)
	}
}

func TestLivenessReviewRetryAfterIsSeparateFromLocalBackoff(t *testing.T) {
	m := &imsRegistrationMaintenance{}
	m.recordRecoveryFailureResult(errors.New("refresh"), voiceclient.RegisterResult{StatusCode: 503, RetryAfter: time.Hour}, errors.New("rejected"))
	state := m.result("fixture").RecoveryState
	if time.Until(state.RetryAfterUntil) < 59*time.Minute || state.NextAttemptAt.Before(state.RetryAfterUntil) {
		t.Fatalf("carrier deadline lost: %+v", state)
	}
	// A later transport failure cannot erase the carrier prohibition.
	m.recordRecoveryFailure(errors.New("transport"), errors.New("timeout"))
	if m.recoveryState.RetryAfterUntil != state.RetryAfterUntil {
		t.Fatal("local backoff erased Retry-After")
	}
	m.mu.Lock()
	m.recordRecoverySuccessLocked()
	m.mu.Unlock()
	if !m.recoveryState.RetryAfterUntil.IsZero() {
		t.Fatal("successful registration retained old holdoff")
	}
}
