// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestReviewFailedMediaAttachmentRetainsUnconfirmedCall(t *testing.T) {
	call := newFakeVoiceCall()
	call.failEnds.Store(1)
	session := newFakeMediaSession()
	session.setConnected(true, time.Now().Add(time.Hour))
	// The lease is ready, but attaching a second stream is rejected.
	session.attached = call
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", &fakeFactory{run: &fakeRuntime{call: call}}, NewMemoryOperationStore(), fakeMediaDirectory{session: session}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		call.failEnds.Store(0)
		_, _ = backend.EndCall(context.Background(), vowifiipc.EndCallRequest{OperationID: "cleanup", CallID: "call-1", ReasonCode: "test_cleanup"})
		close(call.remote)
	})
	if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "dial", CallID: "call-1", Callee: "+100", MediaBufferMS: 500}); operationCode(err) != "call_start_failed" {
		t.Fatalf("attachment error=%v", err)
	}
	backend.mu.Lock()
	retained := backend.activeCall != nil && backend.activeCall.call == call && backend.activeCall.guardCancel != nil
	backend.mu.Unlock()
	if !retained {
		t.Fatal("failed attachment and failed BYE discarded the accepted call")
	}
	reviewAwait(t, func() bool { backend.mu.Lock(); defer backend.mu.Unlock(); return backend.activeCall == nil })
	if call.ends.Load() != 2 {
		t.Fatalf("cleanup attempts=%d", call.ends.Load())
	}
}

func TestReviewLocalHangupStopsRemoteEndObserver(t *testing.T) {
	call := newFakeVoiceCall()
	session := newFakeMediaSession()
	session.setConnected(true, time.Now().Add(time.Hour))
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", &fakeFactory{run: &fakeRuntime{call: call}}, NewMemoryOperationStore(), fakeMediaDirectory{session: session}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "dial", CallID: "call-1", Callee: "+100", MediaBufferMS: 500}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(call.remote) })
	backend.mu.Lock()
	active := backend.activeCall
	backend.mu.Unlock()
	remote := make(chan struct{})
	defer close(remote)
	observed := make(chan struct{})
	go func() { backend.observeRemoteEnd(active, remote); close(observed) }()
	if _, err := backend.EndCall(t.Context(), vowifiipc.EndCallRequest{OperationID: "end", CallID: "call-1", ReasonCode: "user_hangup"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("remote-end observer outlived the confirmed local hangup")
	}
}

type reviewGuardReservationStore struct {
	OperationStore
	fail   atomic.Bool
	denied chan struct{}
}

func (s *reviewGuardReservationStore) Reserve(generation, id, kind string) error {
	if strings.HasPrefix(id, "guard-end-") && s.fail.CompareAndSwap(true, false) {
		close(s.denied)
		return errors.New("injected transient reservation failure")
	}
	return s.OperationStore.Reserve(generation, id, kind)
}

func TestReviewGuardSurvivesTransientReceiptReservationFailure(t *testing.T) {
	call := newFakeVoiceCall()
	session := newFakeMediaSession()
	session.setConnected(true, time.Now().Add(time.Hour))
	store := &reviewGuardReservationStore{OperationStore: NewMemoryOperationStore(), denied: make(chan struct{})}
	store.fail.Store(true)
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", &fakeFactory{run: &fakeRuntime{call: call}}, store, fakeMediaDirectory{session: session}, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "dial", CallID: "call-1", Callee: "+100", MediaBufferMS: 500}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = backend.EndCall(context.Background(), vowifiipc.EndCallRequest{OperationID: "cleanup", CallID: "call-1", ReasonCode: "test_cleanup"})
		close(call.remote)
	})
	session.setConnected(false, time.Now().Add(-time.Minute))
	select {
	case <-store.denied:
	case <-time.After(time.Second):
		t.Fatal("guard did not attempt the receipt reservation")
	}
	reviewAwait(t, func() bool { return session.ended.Load() })
	if call.ends.Load() != 1 {
		t.Fatalf("carrier BYE attempts=%d, want one after storage recovers", call.ends.Load())
	}
}
