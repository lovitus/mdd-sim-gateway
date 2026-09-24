package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestCallTerminalReceiptSurvivesProviderGenerationAndRejectsWrongStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	call := newFakeVoiceCall()
	runtime := &fakeRuntime{call: call}
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", &fakeFactory{run: runtime}, store, fakeMediaDirectory{session: newFakeMediaSession()}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	start := vowifiipc.StartCallRequest{OperationID: "start-original", CallID: "call-1", Callee: "+1000000", MediaBufferMS: 500}
	if _, err = backend.StartCall(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	if _, err = backend.EndCall(t.Context(), vowifiipc.EndCallRequest{OperationID: "end-wrong", CallID: start.CallID, ReasonCode: "user_hangup", ExpectedStartOperationID: "another-start"}); err == nil || call.ends.Load() != 0 {
		t.Fatal("wrong original operation ended call", err)
	}
	if _, err = backend.EndCall(t.Context(), vowifiipc.EndCallRequest{OperationID: "end-original", CallID: start.CallID, ReasonCode: "user_hangup", ExpectedStartOperationID: start.OperationID}); err != nil {
		t.Fatal(err)
	}
	query := vowifiipc.CallReceiptRequest{CallID: start.CallID, OperationID: start.OperationID, SessionID: start.CallID}
	proof, err := backend.CallReceipt(t.Context(), query)
	if err != nil || proof.Source != "confirmed_end" {
		t.Fatal(proof, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restarted, err := NewBackendWithMediaStore("line-1", "native", "process-2", &fakeFactory{run: runtime}, store, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proof, err = restarted.CallReceipt(t.Context(), query)
	if err != nil || proof.ProcessGeneration != "process-1" || call.ends.Load() != 1 {
		t.Fatalf("proof=%+v err=%v ends=%d", proof, err, call.ends.Load())
	}
}
