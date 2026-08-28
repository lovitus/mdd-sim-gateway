// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/callsafety"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/browsermedia"
)

const defaultCallGuardTimeout = 10 * time.Second

type VoiceCall interface {
	browsermedia.Stream
	End(context.Context) (voicehost.DialogInfoResult, error)
}

type VoiceRuntime interface {
	Runtime
	StartMediaCall(context.Context, vowifiipc.StartCallRequest) (VoiceCall, error)
}

type BrowserMediaSession interface {
	Ready() bool
	Connected() bool
	LastSeen() time.Time
	Changes() <-chan struct{}
	AttachStream(browsermedia.Stream) error
	EndStream(string)
}

type MediaDirectory interface {
	Lookup(string) (BrowserMediaSession, bool)
}

type activeVoiceCall struct {
	request     vowifiipc.StartCallRequest
	call        VoiceCall
	session     BrowserMediaSession
	phase       callsafety.Phase
	guardCancel context.CancelFunc
}

func (backend *Backend) StartCall(ctx context.Context, request vowifiipc.StartCallRequest) (vowifiipc.CallResult, error) {
	if err := request.Validate(); err != nil {
		return vowifiipc.CallResult{}, err
	}
	kind := callOperationKind("call_start", request.CallID, request.Callee, fmt.Sprint(request.MediaBufferMS))
	backend.mu.Lock()
	if result, err, found := backend.replayCallLocked(request.OperationID, request.CallID, kind); found || err != nil {
		backend.mu.Unlock()
		return result, err
	}
	if backend.condition != vowifiipc.RuntimeRunning || backend.runtime == nil {
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, notReady("runtime_not_running", "runtime")
	}
	voiceRuntime, ok := backend.runtime.(VoiceRuntime)
	if !ok || !backend.runtime.Layers().Voice.Available {
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, notReady("voice_transport_unavailable", "voice")
	}
	if backend.activeCall != nil {
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, conflictLayer("call_busy", "call")
	}
	if backend.media == nil {
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, notReady("browser_media_transport_unavailable", "media")
	}
	session, found := backend.media.Lookup(request.CallID)
	if !found || !session.Ready() || !session.Connected() {
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, notReady("browser_media_not_ready", "media")
	}
	if err := backend.operations.Reserve(backend.generation, request.OperationID, kind); err != nil {
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, err
	}
	active := &activeVoiceCall{request: request, session: session, phase: callsafety.PhaseDialing}
	backend.activeCall = active
	backend.sequence++
	runtime := backend.runtime
	backend.mu.Unlock()

	call, startErr := voiceRuntime.StartMediaCall(ctx, request)
	if startErr == nil && call == nil {
		startErr = errors.New("voice runtime returned a nil call")
	}
	if startErr == nil {
		startErr = session.AttachStream(call)
	}
	if startErr != nil {
		if call != nil {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, cleanupErr := call.End(cleanupContext)
			cancel()
			startErr = errors.Join(startErr, cleanupErr)
		}
		return backend.failCallStart(active, request.OperationID, startErr)
	}

	backend.mu.Lock()
	if backend.activeCall != active || backend.runtime != runtime || backend.condition != vowifiipc.RuntimeRunning {
		backend.mu.Unlock()
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, cleanupErr := call.End(cleanupContext)
		cancel()
		session.EndStream("call start invalidated")
		return backend.failCallStart(active, request.OperationID,
			errors.Join(errors.New("runtime changed while starting call"), cleanupErr))
	}
	active.call, active.phase = call, callsafety.PhaseActive
	guardContext, guardCancel := context.WithCancel(context.Background())
	active.guardCancel = guardCancel
	backend.sequence++
	result := vowifiipc.CallResult{
		OperationResult: vowifiipc.OperationResult{
			OperationID: request.OperationID, Accepted: true, Code: "active", Status: backend.snapshotLocked(),
		},
		CallID: request.CallID,
	}
	if err := backend.operations.Complete(backend.generation, request.OperationID, result.OperationResult); err != nil {
		active.phase = callsafety.PhaseEnding
		backend.mu.Unlock()
		guardCancel()
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, cleanupErr := call.End(cleanupContext)
		cancel()
		session.EndStream("call state could not be persisted")
		backend.mu.Lock()
		if backend.activeCall == active {
			backend.activeCall = nil
			backend.sequence++
		}
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, errors.Join(err, cleanupErr)
	}
	backend.mu.Unlock()
	go backend.guardCall(guardContext, active)
	return result, nil
}

func (backend *Backend) failCallStart(active *activeVoiceCall, operationID string, cause error) (vowifiipc.CallResult, error) {
	failure := publicFailure(&StageError{Layer: "call", Code: "call_start_failed", Err: cause})
	backend.mu.Lock()
	if backend.activeCall == active {
		backend.activeCall = nil
		backend.sequence++
	}
	storeErr := backend.operations.CompleteFailure(backend.generation, operationID, failure)
	backend.mu.Unlock()
	if storeErr != nil {
		return vowifiipc.CallResult{}, errors.Join(failure, storeErr)
	}
	return vowifiipc.CallResult{}, failure
}

func (backend *Backend) EndCall(ctx context.Context, request vowifiipc.EndCallRequest) (vowifiipc.CallResult, error) {
	if err := request.Validate(); err != nil {
		return vowifiipc.CallResult{}, err
	}
	kind := callOperationKind("call_end", request.CallID, request.ReasonCode)
	backend.mu.Lock()
	if result, err, found := backend.replayCallLocked(request.OperationID, request.CallID, kind); found || err != nil {
		backend.mu.Unlock()
		return result, err
	}
	active := backend.activeCall
	if active == nil || active.request.CallID != request.CallID {
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, &vowifiipc.OperationError{
			Kind: vowifiipc.ErrorNotFound, Code: "call_not_found", Layer: "call",
		}
	}
	if active.call == nil {
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, conflictLayer("call_start_in_progress", "call")
	}
	if err := backend.operations.Reserve(backend.generation, request.OperationID, kind); err != nil {
		backend.mu.Unlock()
		return vowifiipc.CallResult{}, err
	}
	active.phase = callsafety.PhaseEnding
	if active.guardCancel != nil {
		active.guardCancel()
		active.guardCancel = nil
	}
	backend.sequence++
	backend.mu.Unlock()

	_, endErr := active.call.End(ctx)
	backend.mu.Lock()
	if endErr != nil {
		failure := publicFailure(&StageError{Layer: "call", Code: "call_end_failed", Err: endErr})
		storeErr := backend.operations.CompleteFailure(backend.generation, request.OperationID, failure)
		backend.mu.Unlock()
		if storeErr != nil {
			return vowifiipc.CallResult{}, errors.Join(failure, storeErr)
		}
		return vowifiipc.CallResult{}, failure
	}
	active.phase = callsafety.PhaseEnded
	backend.activeCall = nil
	backend.sequence++
	result := vowifiipc.CallResult{
		OperationResult: vowifiipc.OperationResult{
			OperationID: request.OperationID, Accepted: true, Code: "ended", Status: backend.snapshotLocked(),
		},
		CallID: request.CallID,
	}
	storeErr := backend.operations.Complete(backend.generation, request.OperationID, result.OperationResult)
	backend.mu.Unlock()
	active.session.EndStream("call ended")
	if storeErr != nil {
		return vowifiipc.CallResult{}, storeErr
	}
	return result, nil
}

func (backend *Backend) guardCall(ctx context.Context, active *activeVoiceCall) {
	guard := callsafety.Guard{HeartbeatTimeout: backend.callGuardTimeout}
	for {
		backend.mu.Lock()
		if backend.activeCall != active {
			backend.mu.Unlock()
			return
		}
		phase := active.phase
		backend.mu.Unlock()
		lastSeen, connected := active.session.LastSeen(), active.session.Connected()
		decision := guard.Evaluate(callsafety.Call{
			ID: active.request.CallID, Phase: phase,
			BrowserLastSeen: lastSeen, BrowserConnected: connected,
		}, time.Now())
		if decision.Action == callsafety.ActionHangupExact {
			operationID := "guard-end-" + callDigest(active.request.CallID+"\x00"+active.request.OperationID)
			endContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = backend.EndCall(endContext, vowifiipc.EndCallRequest{
				OperationID: operationID, CallID: active.request.CallID, ReasonCode: "browser_timeout",
			})
			cancel()
			return
		}
		deadline := lastSeen.Add(backend.callGuardTimeout)
		wait := time.Until(deadline)
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-active.session.Changes():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (backend *Backend) replayCallLocked(operationID, callID, kind string) (vowifiipc.CallResult, error, bool) {
	result, err, found := backend.replayLocked(operationID, kind)
	if !found || err != nil {
		return vowifiipc.CallResult{}, err, found
	}
	return vowifiipc.CallResult{OperationResult: result, CallID: callID}, nil, true
}

func callOperationKind(prefix string, values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(value))
	}
	return prefix + ":" + hex.EncodeToString(digest.Sum(nil))
}

func callDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}
