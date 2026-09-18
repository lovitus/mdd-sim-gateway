// SPDX-License-Identifier: AGPL-3.0-only
package service

import (
	"context"
	"errors"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func observedTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	value := at.UTC()
	return &value
}

func (runtime *upstreamRuntime) StatusWithHealth() (Layers, *vowifiipc.RuntimeHealth) {
	registration, _ := runtime.registrationSnapshot()
	health := &vowifiipc.RuntimeHealth{IMSRegistered: registration.Registered, IMSExpiresAt: observedTime(registration.ExpiresAt),
		IMSStatusCode: registration.StatusCode, IMSFailures: registration.RecoveryState.ConsecutiveFailures,
		IMSRecovering: registration.RecoveryState.InProgress, IMSNextAttemptAt: observedTime(registration.RecoveryState.NextAttemptAt),
		IMSRetryAfterUntil: observedTime(registration.RecoveryState.RetryAfterUntil)}
	if health.IMSFailures > 0 {
		health.IMSFailureCode = "ims_register_transport_failed"
		if health.IMSStatusCode >= 400 {
			health.IMSFailureCode = "ims_register_rejected"
		}
	}
	if snapshot, ok := runtime.packetSession.LivenessSnapshot(); ok {
		health.LastInboundAt, health.LastDPDSuccessAt = observedTime(snapshot.LastInbound), observedTime(snapshot.LastDPDSuccess)
		health.DPDEnabled, health.DPDDead, health.MissedDPDProbes = snapshot.DPDEnabled, snapshot.Dead, snapshot.MissedDPDProbes
	}
	return runtime.registrationLayers(registration), health
}

func registrationProgressCode(err error) string {
	if errors.Is(err, runtimehost.ErrIMSRegistrationInProgress) {
		return "ims_recovering"
	}
	if errors.Is(err, runtimehost.ErrIMSRegistrationRetryPending) {
		return "ims_retry_wait"
	}
	return ""
}

func (backend *Backend) registrationProgressLocked(operationID, code string) vowifiipc.OperationResult {
	return vowifiipc.OperationResult{OperationID: operationID, Accepted: true, Code: code, Status: backend.snapshotLocked()}
}

func (backend *Backend) idleRegistrationBarrierLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, ok := backend.runtime.(interface {
		StatusWithHealth() (Layers, *vowifiipc.RuntimeHealth)
	})
	if !ok {
		return nil
	}
	_, health := source.StatusWithHealth()
	snapshot := vowifiipc.Snapshot{Runtime: vowifiipc.RuntimeStatus{Health: health}}
	if snapshot.IdleRecoveryBlockedAt(time.Now()) {
		if snapshot.Runtime.Health.IMSRecovering {
			return conflict("operation_in_progress")
		}
		delay := time.Until(*snapshot.Runtime.Health.IMSRetryAfterUntil)
		if delay <= 0 {
			return nil
		}
		return &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "ims_retry_wait", Layer: "ims", RetryAfter: delay, RetryAfterMS: delay.Milliseconds()}
	}
	return nil
}
