// SPDX-License-Identifier: AGPL-3.0-only
package provider

import (
	"context"
	"errors"
	"time"

	swu "github.com/boa-z/vowifi-go/engine/swu"
)

var ErrIKELivenessDead = errors.New("authenticated IKE liveness probe budget exhausted")

type livenessSession interface {
	AdvanceIKELiveness(context.Context, time.Time) (swu.IKELivenessDecision, error)
	IKELivenessSnapshot() swu.IKELivenessSnapshot
}

func (session *Session) LivenessSnapshot() (swu.IKELivenessSnapshot, bool) {
	if session == nil || session.base == nil {
		return swu.IKELivenessSnapshot{}, false
	}
	scheduler, ok := session.base.(livenessSession)
	if !ok {
		return swu.IKELivenessSnapshot{}, false
	}
	return scheduler.IKELivenessSnapshot(), true
}

// RunLivenessMaintenance only drives the existing upstream state machine.
// It is independent of both rekey timers. Silence schedules a probe; only
// the upstream authenticated probe result/budget can declare the tunnel dead.
func (session *Session) RunLivenessMaintenance(ctx context.Context) error {
	if ctx == nil {
		return errors.New("liveness maintenance context is required")
	}
	if session == nil || session.base == nil {
		return ErrProviderSessionClose
	}
	scheduler, ok := session.base.(livenessSession)
	if !ok {
		return nil
	}
	if !session.livenessRunning.CompareAndSwap(false, true) {
		return errors.New("liveness maintenance already running")
	}
	defer session.livenessRunning.Store(false)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if session.closed.Load() {
			return ErrProviderSessionClose
		}
		decision, _ := scheduler.AdvanceIKELiveness(ctx, time.Now())
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := scheduler.IKELivenessSnapshot()
		if decision.Dead || snapshot.Dead {
			return ErrIKELivenessDead
		}
		if !snapshot.DPDEnabled && !snapshot.KeepaliveEnabled {
			return nil
		}
		// Errors from an individual probe/keepalive are not proof of death.
		// The upstream decision owns retry timing and the consecutive-failure count.
		wait := time.Until(decision.NextDue)
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
