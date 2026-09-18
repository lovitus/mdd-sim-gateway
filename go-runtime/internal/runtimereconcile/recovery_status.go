package runtimereconcile

import (
	"fmt"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

// Project the existing owner's schedule; this never advances a timer or
// schedules an operation. Provider and Core observations remain distinguishable.
func (r *Reconciler) recoveryScheduleDetail(catalogLine linecatalog.Line, o lineObservation) string {
	if !o.status.RequiresIdleRecovery() || !catalogLine.Enabled || !o.intentEnabled || o.status.Maintenance.Draining {
		return ""
	}
	var policy recovery.ContinuousRetry
	if catalogLine.Retry != nil {
		policy = *catalogLine.Retry
	} else if r.continuousRetry != nil {
		var err error
		policy, err = r.continuousRetry()
		if err != nil {
			return ""
		}
	}
	r.mu.Lock()
	line := r.lines[catalogLine.ID]
	if line == nil {
		r.mu.Unlock()
		return ""
	}
	start, next, active, attempts := line.failureWindowStart, line.recoveryNext, line.inFlight, line.recoveryFailures
	if line.next.After(next) {
		next = line.next
	}
	r.mu.Unlock()
	fields := []string{fmt.Sprintf("core_recovering=%t", active), fmt.Sprintf("core_recovery_attempts=%d", attempts)}
	if budget, err := policy.Budget(); err == nil && !start.IsZero() {
		fields = append(fields, fmt.Sprintf("core_failure_count=%d", policy.Count(r.now().Sub(start))), fmt.Sprintf("core_failure_max=%d", policy.Max))
		if due := start.Add(budget); due.After(next) {
			next = due
		}
	}
	if h := o.status.Runtime.Health; h != nil && h.IMSRetryAfterUntil != nil && h.IMSRetryAfterUntil.After(next) {
		next = *h.IMSRetryAfterUntil
	}
	if !next.IsZero() {
		fields = append(fields, "core_recovery_due_at="+next.UTC().Format(time.RFC3339Nano))
	}
	return strings.Join(fields, ";")
}
