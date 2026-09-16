// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/callsafety"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestReviewFailedRuntimeStopRestoresCallGuard(t *testing.T) {
	call := newFakeVoiceCall()
	call.failEnds.Store(1)
	session := newFakeMediaSession()
	session.setConnected(true, time.Now().Add(time.Hour))
	runtime := &fakeRuntime{call: call}
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", &fakeFactory{run: runtime}, NewMemoryOperationStore(), fakeMediaDirectory{session: session}, 30*time.Millisecond)
	if err != nil { t.Fatal(err) }
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil { t.Fatal(err) }
	if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "dial", CallID: "call-1", Callee: "+100", MediaBufferMS: 500}); err != nil { t.Fatal(err) }
	t.Cleanup(func() { call.failEnds.Store(0); _, _ = backend.EndCall(context.Background(), vowifiipc.EndCallRequest{OperationID: "cleanup", CallID: "call-1", ReasonCode: "test_cleanup"}) })
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "stop"}); operationCode(err) != "call_end_failed" { t.Fatalf("stop error=%v", err) }
	backend.mu.Lock()
	active := backend.activeCall
	guarded := active != nil && active.call == call && active.guardCancel != nil && active.phase == callsafety.PhaseActive
	backend.mu.Unlock()
	if !guarded { t.Fatal("failed Stop left the retained paid call without an active safety guard") }
	if runtime.closes.Load() != 0 { t.Fatal("runtime closed before call termination was confirmed") }
	session.setConnected(false, time.Now().Add(-time.Minute))
	reviewAwait(t, func() bool { return session.ended.Load() })
	if call.ends.Load() != 2 { t.Fatalf("BYE attempts=%d, want exactly one guarded retry", call.ends.Load()) }
}

type reviewFailedCallCommit struct{ OperationStore }
func (s reviewFailedCallCommit) Complete(generation, id string, result vowifiipc.OperationResult) error {
	if id == "dial" { return errors.New("injected call receipt failure") }
	return s.OperationStore.Complete(generation, id, result)
}

func TestReviewFailedCallCommitRetainsUnconfirmedCall(t *testing.T) {
	call := newFakeVoiceCall()
	call.failEnds.Store(1)
	session := newFakeMediaSession()
	// A fresh browser must not suppress retrying cleanup of a failed start.
	session.setConnected(true, time.Now().Add(time.Hour))
	store := reviewFailedCallCommit{NewMemoryOperationStore()}
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", &fakeFactory{run: &fakeRuntime{call: call}}, store, fakeMediaDirectory{session: session}, time.Second)
	if err != nil { t.Fatal(err) }
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil { t.Fatal(err) }
	t.Cleanup(func() { call.failEnds.Store(0); _, _ = backend.EndCall(context.Background(), vowifiipc.EndCallRequest{OperationID: "cleanup", CallID: "call-1", ReasonCode: "test_cleanup"}) })
	if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "dial", CallID: "call-1", Callee: "+100", MediaBufferMS: 500}); err == nil { t.Fatal("call receipt failure was hidden") }
	backend.mu.Lock()
	active := backend.activeCall
	retained := active != nil && active.call == call && active.guardCancel != nil
	backend.mu.Unlock()
	if !retained { t.Fatal("failed receipt and failed BYE discarded the live call handle") }
	reviewAwait(t, func() bool { backend.mu.Lock(); defer backend.mu.Unlock(); return backend.activeCall == nil })
	if call.ends.Load() != 2 { t.Fatalf("cleanup attempts=%d, want 2", call.ends.Load()) }
}

type reviewFactory struct{ runtime Runtime }
func (f reviewFactory) Start(context.Context) (Runtime, error) { return f.runtime, nil }

type reviewVoiceRuntime struct {
	*fakeRuntime
	calls map[string]VoiceCall
}
func (r *reviewVoiceRuntime) StartMediaCall(_ context.Context, request vowifiipc.StartCallRequest) (VoiceCall, error) { return r.calls[request.CallID], nil }

type reviewMediaDirectory map[string]*fakeMediaSession
func (d reviewMediaDirectory) Lookup(id string) (BrowserMediaSession, bool) { s, ok := d[id]; return s, ok }

type reviewBlockingCall struct {
	*fakeVoiceCall
	entered chan struct{}
	release chan struct{}
	once sync.Once
}
func (c *reviewBlockingCall) End(ctx context.Context) (voicehost.DialogInfoResult, error) {
	c.once.Do(func() { close(c.entered) })
	select {
	case <-c.release: return c.fakeVoiceCall.End(ctx)
	case <-ctx.Done(): return voicehost.DialogInfoResult{}, ctx.Err()
	}
}

func TestReviewLateHangupCannotClearReplacementCall(t *testing.T) {
	old := &reviewBlockingCall{fakeVoiceCall: newFakeVoiceCall(), entered: make(chan struct{}), release: make(chan struct{})}
	var unblock sync.Once
	release := func() { unblock.Do(func() { close(old.release) }) }
	t.Cleanup(release)
	next := newFakeVoiceCall()
	firstSession, nextSession := newFakeMediaSession(), newFakeMediaSession()
	firstSession.setConnected(true, time.Now().Add(time.Hour))
	nextSession.setConnected(true, time.Now().Add(time.Hour))
	runtime := &reviewVoiceRuntime{fakeRuntime: &fakeRuntime{}, calls: map[string]VoiceCall{"call-1": old, "call-2": next}}
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", reviewFactory{runtime}, NewMemoryOperationStore(), reviewMediaDirectory{"call-1": firstSession, "call-2": nextSession}, time.Second)
	if err != nil { t.Fatal(err) }
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil { t.Fatal(err) }
	if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "dial-1", CallID: "call-1", Callee: "+100", MediaBufferMS: 500}); err != nil { t.Fatal(err) }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ended := make(chan error, 1)
	go func() { _, err := backend.EndCall(ctx, vowifiipc.EndCallRequest{OperationID: "end-1", CallID: "call-1", ReasonCode: "user_hangup"}); ended <- err }()
	select { case <-old.entered: case <-ctx.Done(): t.Fatal("BYE did not start") }
	// A remote termination can release the old call while its BYE reply is delayed.
	close(old.remote)
	reviewAwait(t, func() bool { return firstSession.ended.Load() })
	if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "dial-2", CallID: "call-2", Callee: "+200", MediaBufferMS: 500}); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _, _ = backend.EndCall(context.Background(), vowifiipc.EndCallRequest{OperationID: "cleanup-2", CallID: "call-2", ReasonCode: "test_cleanup"}); close(next.remote) })
	release()
	if err := <-ended; err != nil { t.Fatal(err) }
	backend.mu.Lock()
	retained := backend.activeCall != nil && backend.activeCall.call == next && backend.activeCall.guardCancel != nil
	backend.mu.Unlock()
	if !retained || nextSession.ended.Load() { t.Fatal("late completion of call-1 removed ownership of call-2") }
}

func reviewAwait(t *testing.T, ready func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select { case <-ctx.Done(): t.Fatal("timed out waiting for the exact call transition"); case <-ticker.C: }
	}
}
