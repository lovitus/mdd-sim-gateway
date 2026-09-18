package service

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type failedMessageCommitStore struct{ OperationStore }

func (failedMessageCommitStore) CompleteMessage(string, vowifiipc.OperationResult, *vowifiipc.OperationError) error {
	return errors.New("simulated disk failure after network submission")
}

func TestPaidMessageSurvivesDifferentProcessGeneration(t *testing.T) {
	for _, state := range []string{"completed", "uncertain", "failed"} {
		t.Run(state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operations.db")
			store, err := OpenBoltOperationStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			runtime := &fakeRuntime{}
			var operations OperationStore = store
			if state == "uncertain" {
				operations = failedMessageCommitStore{store}
			}
			if state == "failed" {
				runtime.messageErr = errors.New("submission failed")
			}
			newBackend := func(generation, card string, run *fakeRuntime, records OperationStore) *Backend {
				t.Helper()
				backend, err := NewBackendWithMediaStore("line-1", "native", generation, &fakeFactory{run: run}, records, nil, time.Second, card)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start-1"}); err != nil {
					t.Fatal(err)
				}
				return backend
			}
			first := newBackend("generation-1", "8944000000000000001", runtime, operations)
			request := vowifiipc.SendMessageRequest{OperationID: "paid-1", MessageID: "message-1", Recipient: "+100", Body: "fixture"}
			_, firstErr := first.SendMessage(t.Context(), request)
			if (firstErr == nil) != (state == "completed") || runtime.messages.Load() != 1 {
				t.Fatalf("first submission count=%d err=%v", runtime.messages.Load(), firstErr)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenBoltOperationStore(path)
			if err != nil {
				t.Fatal(err)
			}
			secondRuntime := &fakeRuntime{}
			second := newBackend("generation-2", "8944000000000000001", secondRuntime, store)
			result, replayErr := second.SendMessage(t.Context(), request)
			switch state {
			case "completed":
				if replayErr != nil || result.Code != "sent" || result.Status.ProcessGeneration != "generation-2" {
					t.Fatalf("replay=%+v err=%v", result, replayErr)
				}
			case "uncertain":
				if operationCode(replayErr) != "operation_result_unknown" {
					t.Fatalf("uncertain err=%v", replayErr)
				}
			case "failed":
				if operationCode(replayErr) != "message_send_failed" {
					t.Fatalf("failed err=%v", replayErr)
				}
			}
			changed := request
			changed.Body = "different request"
			if _, err := second.SendMessage(t.Context(), changed); operationCode(err) != "operation_id_reused" {
				t.Fatalf("fingerprint err=%v", err)
			}
			replacementRuntime := &fakeRuntime{}
			replacement := newBackend("generation-3", "8944000000000000002", replacementRuntime, store)
			if _, err := replacement.SendMessage(t.Context(), request); operationCode(err) != "operation_id_reused" {
				t.Fatalf("SIM replacement err=%v", err)
			}
			if secondRuntime.messages.Load() != 0 || replacementRuntime.messages.Load() != 0 {
				t.Fatal("message was transmitted again")
			}
		})
	}
}

func TestLegacyMessageReceiptRemainsUnknownAcrossUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	request := vowifiipc.SendMessageRequest{OperationID: "paid-1", MessageID: "message-1", Recipient: "+100", Body: "fixture"}
	if err := store.Reserve("old-generation", request.OperationID, operationKind("message_send", request.MessageID, request.Recipient, request.Body)); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("old-generation", request.OperationID, vowifiipc.OperationResult{OperationID: request.OperationID, Accepted: true, Code: "sent", Status: validStoreSnapshot()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &fakeRuntime{}
	backend, err := NewBackendWithStore("line-1", "native", "new-generation", &fakeFactory{run: runtime}, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.SendMessage(t.Context(), request); operationCode(err) != "operation_result_unknown" || runtime.messages.Load() != 0 {
		t.Fatalf("legacy err=%v sends=%d", err, runtime.messages.Load())
	}
	old, found, err := store.Lookup("old-generation", request.OperationID)
	if err != nil || !found || !old.Done || old.Result.Code != "sent" {
		t.Fatal("original receipt was destroyed")
	}
}

func TestRegistrationOwnsAdmissionUntilCompletion(t *testing.T) {
	for _, fail := range []bool{false, true} {
		runtime := &fakeRuntime{registerStarted: make(chan struct{}, 1), registerRelease: make(chan struct{})}
		if fail {
			runtime.registerErr = errors.New("registration rejected")
		}
		backend, err := NewBackend("line-1", "native", "process-1", &fakeFactory{run: runtime})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := backend.Register(t.Context(), vowifiipc.RegisterRequest{OperationID: "register"})
			done <- err
		}()
		select {
		case <-runtime.registerStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("registration did not start")
		}
		checks := []func() error{
			func() error {
				_, err := backend.BeginDrain(t.Context(), vowifiipc.MaintenanceRequest{LeaseID: "drain"})
				return err
			},
			func() error {
				_, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "stop"})
				return err
			},
			func() error {
				_, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "recover", RequireIdle: true})
				return err
			},
			func() error {
				_, err := backend.SendMessage(t.Context(), vowifiipc.SendMessageRequest{OperationID: "sms", MessageID: "message", Recipient: "+100", Body: "fixture"})
				return err
			},
			func() error {
				_, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "dial", CallID: "call", Callee: "+100", MediaBufferMS: 500})
				return err
			},
		}
		for _, check := range checks {
			if err := check(); operationCode(err) != "operation_in_progress" {
				t.Errorf("concurrent operation err=%v", err)
			}
		}
		if progress, err := backend.Register(t.Context(), vowifiipc.RegisterRequest{OperationID: "second-register"}); err != nil || progress.Code != "ims_recovering" {
			t.Errorf("second registration progress=%+v err=%v", progress, err)
		}
		close(runtime.registerRelease)
		if err := <-done; (err != nil) != fail {
			t.Fatalf("registration result=%v", err)
		}
		if _, err := backend.BeginDrain(t.Context(), vowifiipc.MaintenanceRequest{LeaseID: "drain"}); err != nil {
			t.Fatal(err)
		}
		if runtime.closes.Load() != 0 || runtime.messages.Load() != 0 || runtime.callStarts.Load() != 0 {
			t.Fatal("in-flight registration allowed conflicting side effects")
		}
	}
}

func TestFailedStopCannotLoseOwnedRuntimeToStart(t *testing.T) {
	runtime := &fakeRuntime{closeErr: errors.New("resources still owned")}
	factory := &fakeFactory{run: runtime}
	backend, err := NewBackend("line-1", "native", "process-1", factory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "stop"}); err == nil {
		t.Fatal("stop unexpectedly succeeded")
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "replace"}); operationCode(err) != "runtime_busy" {
		t.Fatalf("start err=%v", err)
	}
	if factory.starts.Load() != 1 || backend.runtime != runtime {
		t.Fatal("retained runtime ownership lost")
	}
	runtime.closeErr = nil
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "cleanup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "restart"}); err != nil {
		t.Fatal(err)
	}
}
