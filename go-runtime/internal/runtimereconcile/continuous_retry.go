package runtimereconcile

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"time"
)

// Source: ec620942 main.py:_health_recovery_due/apply_health. Keep this
// continuous failure budget separate from transport backoff and exit strikes.
func (reconciler *Reconciler) continuousFailureDue(catalogLine linecatalog.Line, observation lineObservation) bool {
	var policy recovery.ContinuousRetry
	if catalogLine.Retry != nil {
		policy = *catalogLine.Retry
	} else if reconciler.continuousRetry != nil {
		var err error
		policy, err = reconciler.continuousRetry()
		if err != nil {
			reconciler.clearFailureWindow(catalogLine.ID)
			return false
		}
	} else {
		return true
	}
	budget, err := policy.Budget()
	if err != nil {
		reconciler.clearFailureWindow(catalogLine.ID)
		return false
	}
	now := reconciler.now()
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	line := reconciler.lineLocked(catalogLine.ID)
	if line.failureWindowStart.IsZero() || line.failureWindowFence != observation.fence || line.failureWindowEpoch != observation.intentEpoch || now.Before(line.failureWindowStart) {
		line.failureWindowStart = now
		line.failureWindowFence = observation.fence
		line.failureWindowEpoch = observation.intentEpoch
	}
	return now.Sub(line.failureWindowStart) >= budget
}

func (reconciler *Reconciler) clearFailureWindow(lineID string) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	line := reconciler.lineLocked(lineID)
	line.failureWindowStart = time.Time{}
	line.failureWindowFence = mediaauth.ProviderFence{}
	line.failureWindowEpoch = 0
}
