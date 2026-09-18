package runtimehost

import (
	"context"
	"errors"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"testing"
	"time"
)

func TestLivenessReviewExpiredRegistrationIsNotReady(t *testing.T) {
	at := time.Now().Add(-2 * time.Minute)
	m := &imsRegistrationMaintenance{registered: true, statusCode: 200, registeredAt: at, session: voiceclient.RegisterSession{Expires: 60}}
	result := m.result("fixture")
	if result.Registered {
		t.Fatal("expired IMS registration remains ready")
	}
	if result.ExpiresAt.IsZero() || result.ExpiresAt.After(time.Now()) {
		t.Fatal("expiry evidence was lost")
	}
	if m.registeredAt != at {
		t.Fatal("observing state refreshed the registration lease")
	}
}

func TestLivenessReviewCancelledManualRecoveryDoesNotWaitForOwner(t *testing.T) {
	m := &imsRegistrationMaintenance{}
	m.operationMu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Recover(ctx); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancel=%v", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Error("cancelled manual recovery queued behind maintenance")
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.operationMu.Unlock()
}

func TestLivenessReviewCloseHonorsDeadlineWhileMaintenanceBusy(t *testing.T) {
	m := &imsRegistrationMaintenance{flow: &voiceclient.WireSIPFlow{}}
	m.operationMu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Close(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("close=%v", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Error("Close ignored deadline behind maintenance lock")
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.operationMu.Unlock()
}

func TestLivenessReviewInitialRegisterPreservesRetryAfter(t *testing.T) {
	result := imsRegisterFailureResult(voiceclient.RegisterResult{StatusCode: 503, RetryAfter: time.Hour}, voiceclient.IMSProfile{}, errors.New("rejected"))
	if time.Until(result.RecoveryState.NextAttemptAt) < 59*time.Minute {
		t.Fatal("initial REGISTER discarded Retry-After evidence")
	}
}
