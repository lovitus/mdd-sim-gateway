// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type remediationFactory struct {
	runtime Runtime
}

func (factory *remediationFactory) Start(context.Context) (Runtime, error) {
	return factory.runtime, nil
}

type remediationRuntime struct {
	*fakeRuntime
	startCall VoiceCall
	startErr  error
}

func (runtime *remediationRuntime) StartMediaCall(context.Context, vowifiipc.StartCallRequest) (VoiceCall, error) {
	runtime.callStarts.Add(1)
	return runtime.startCall, runtime.startErr
}

type receiptFaultStore struct {
	OperationStore
	failNext atomic.Bool
}

func (store *receiptFaultStore) SaveCallReceipt(scope string, receipt vowifiipc.CallReceipt) error {
	if store.failNext.CompareAndSwap(true, false) {
		return errors.New("injected receipt write failure")
	}
	return store.OperationStore.SaveCallReceipt(scope, receipt)
}

func remediationBackend(t *testing.T, runtime *remediationRuntime, store OperationStore) (*Backend, *fakeMediaSession) {
	t.Helper()
	session := newFakeMediaSession()
	backend, err := NewBackendWithMediaStore(
		"line-1", "native", "process-1", &remediationFactory{runtime: runtime}, store,
		fakeMediaDirectory{session: session}, 15*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	return backend, session
}

func remediationStart() vowifiipc.StartCallRequest {
	return vowifiipc.StartCallRequest{OperationID: "call-start", CallID: "call-1", Callee: "+100", MediaBufferMS: 500}
}

func TestDefinitiveSIPRejectionPersistsBoundRejectedReceipt(t *testing.T) {
	store := NewMemoryOperationStore()
	runtime := &remediationRuntime{
		fakeRuntime: &fakeRuntime{},
		startErr: &definitiveCallRejection{failure: &vowifiipc.OperationError{
			Kind: vowifiipc.ErrorRejected, Code: "call_rejected", Layer: "voice", Detail: "486 Busy Here",
		}},
	}
	backend, _ := remediationBackend(t, runtime, store)
	if _, err := backend.StartCall(t.Context(), remediationStart()); operationCode(err) != "call_rejected" {
		t.Fatalf("StartCall rejection = %v", err)
	}
	request := vowifiipc.CallReceiptRequest{CallID: "call-1", OperationID: "call-start", SessionID: "call-1"}
	receipt, err := backend.CallReceipt(t.Context(), request)
	if err != nil || receipt.Source != "confirmed_rejected" || receipt.TerminalOutcome() != "rejected" || receipt.Validate() != nil {
		t.Fatalf("receipt=%+v outcome=%q err=%v", receipt, receipt.TerminalOutcome(), err)
	}
	if runtime.callStarts.Load() != 1 || backend.activeCall != nil {
		t.Fatalf("starts=%d active=%v", runtime.callStarts.Load(), backend.activeCall)
	}
}

func TestStopPersistsExactTerminalReceiptBeforeRuntimeClose(t *testing.T) {
	store := NewMemoryOperationStore()
	call := newFakeVoiceCall()
	runtime := &remediationRuntime{fakeRuntime: &fakeRuntime{call: call}, startCall: call}
	backend, session := remediationBackend(t, runtime, store)
	if _, err := backend.StartCall(t.Context(), remediationStart()); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-stop"}); err != nil {
		t.Fatal(err)
	}
	receipt, err := backend.CallReceipt(t.Context(), vowifiipc.CallReceiptRequest{CallID: "call-1", OperationID: "call-start", SessionID: "call-1"})
	if err != nil || receipt.Source != "confirmed_end" || receipt.TerminalOutcome() != "ended" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if call.ends.Load() != 1 || runtime.closes.Load() != 1 || !session.ended.Load() {
		t.Fatalf("ends=%d closes=%d sessionEnded=%v", call.ends.Load(), runtime.closes.Load(), session.ended.Load())
	}
}

func TestStopReceiptFailureRetainsOwnerAndRetriesWithoutAnotherBye(t *testing.T) {
	base := NewMemoryOperationStore()
	store := &receiptFaultStore{OperationStore: base}
	store.failNext.Store(true)
	call := newFakeVoiceCall()
	runtime := &remediationRuntime{fakeRuntime: &fakeRuntime{call: call}, startCall: call}
	backend, _ := remediationBackend(t, runtime, store)
	if _, err := backend.StartCall(t.Context(), remediationStart()); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-stop"}); operationCode(err) != "call_terminal_persist_failed" {
		t.Fatalf("Stop error=%v", err)
	}
	reviewAwait(t, func() bool {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		return backend.activeCall == nil
	})
	request := vowifiipc.CallReceiptRequest{CallID: "call-1", OperationID: "call-start", SessionID: "call-1"}
	receipt, err := backend.CallReceipt(t.Context(), request)
	if err != nil || receipt.Source != "confirmed_end" || call.ends.Load() != 1 || runtime.closes.Load() != 0 {
		t.Fatalf("receipt=%+v ends=%d closes=%d err=%v", receipt, call.ends.Load(), runtime.closes.Load(), err)
	}
}
