// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/callsafety"
)

// restartCallGuardLocked keeps the exact call owned after a failed BYE.
// A failed start has no usable owner, so cleanup must retry even while the
// browser remains connected. Ordinary failed hangup preserves heartbeat policy.
func (backend *Backend) restartCallGuardLocked(active *activeVoiceCall, cleanup bool) context.Context {
	if backend.activeCall != active || active.call == nil && !active.terminationConfirmed {
		return nil
	}
	if active.guardCancel != nil {
		active.guardCancel()
	}
	active.phase = callsafety.PhaseActive
	active.cleanupPending = active.cleanupPending || cleanup
	active.guardRetryAt = time.Now().Add(guardRetryDelay(active.guardAttempt, backend.callGuardTimeout))
	ctx, cancel := context.WithCancel(context.Background())
	active.guardCancel = cancel
	backend.sequence++
	return ctx
}

func (backend *Backend) retainCallCleanup(active *activeVoiceCall, call VoiceCall) {
	backend.mu.Lock()
	if backend.activeCall != active {
		backend.mu.Unlock()
		return
	}
	active.call = call
	ctx := backend.restartCallGuardLocked(active, true)
	backend.mu.Unlock()
	active.session.EndStream("call cleanup unconfirmed")
	if ctx != nil {
		go backend.guardCall(ctx, active)
		if remote, ok := call.(interface{ RemoteEnded() <-chan struct{} }); ok {
			go backend.observeRemoteEnd(active, remote.RemoteEnded())
		}
	}
}

// finishCallLocked retires only this call's lifetime. A delayed carrier reply
// must never clear a replacement installed after an earlier remote hangup.
func (backend *Backend) finishCallLocked(active *activeVoiceCall) {
	if active.guardCancel != nil {
		active.guardCancel()
		active.guardCancel = nil
	}
	active.phase = callsafety.PhaseEnded
	active.cleanupPending = false
	if active.done != nil {
		select {
		case <-active.done:
		default:
			close(active.done)
		}
	}
	if backend.activeCall == active {
		backend.activeCall = nil
		backend.sequence++
	}
}
