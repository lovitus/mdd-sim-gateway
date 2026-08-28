// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type fakeFactory struct {
	starts atomic.Int32
	run    *fakeRuntime
	err    error
}

func (factory *fakeFactory) Start(context.Context) (Runtime, error) {
	factory.starts.Add(1)
	if factory.err != nil {
		return nil, factory.err
	}
	if factory.run == nil {
		return nil, nil
	}
	return factory.run, factory.err
}

type fakeRuntime struct{ closes atomic.Int32 }

func (*fakeRuntime) Layers() Layers {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	return Layers{Tunnel: ready, IMS: ready, Voice: ready, Messaging: ready}
}
func (runtime *fakeRuntime) Close(context.Context) error { runtime.closes.Add(1); return nil }

func TestBackendLifecycleAndIdempotency(t *testing.T) {
	runtime := &fakeRuntime{}
	factory := &fakeFactory{run: runtime}
	backend, err := NewBackend("line-1", "native", "process-1", factory)
	if err != nil {
		t.Fatal(err)
	}
	started, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"})
	if err != nil || started.Status.Runtime.Condition != vowifiipc.RuntimeRunning || !started.Status.Voice.Available {
		t.Fatalf("start=%+v err=%v", started, err)
	}
	replayed, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"})
	if err != nil || replayed.Code != "started" || factory.starts.Load() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", replayed, factory.starts.Load(), err)
	}
	_, err = backend.Stop(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"})
	var operationErr *vowifiipc.OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != "operation_id_reused" {
		t.Fatalf("reuse err=%v", err)
	}
	stopped, err := backend.Stop(context.Background(), vowifiipc.LifecycleRequest{OperationID: "stop-1"})
	if err != nil || stopped.Status.Runtime.Condition != vowifiipc.RuntimeStopped || runtime.closes.Load() != 1 {
		t.Fatalf("stop=%+v closes=%d err=%v", stopped, runtime.closes.Load(), err)
	}
}

func TestBackendFailedStartIsRetryableByNewOperation(t *testing.T) {
	factory := &fakeFactory{err: &StageError{Layer: "ims", Code: "ims_register_failed", Err: errors.New("rejected")}}
	backend, err := NewBackend("line-1", "native", "process-1", factory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"}); err == nil {
		t.Fatal("first start err=nil")
	}
	status, err := backend.Status(context.Background())
	if err != nil || status.Runtime.Condition != vowifiipc.RuntimeFailed || status.Runtime.Code != "ims_register_failed" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	factory.err = nil
	factory.run = &fakeRuntime{}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-2"}); err != nil {
		t.Fatalf("retry start: %v", err)
	}
}

func TestBackendDoesNotExposePaidActionsBeforeTransportExists(t *testing.T) {
	backend, err := NewBackend("line-1", "native", "process-1", &fakeFactory{run: &fakeRuntime{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.StartCall(context.Background(), vowifiipc.StartCallRequest{})
	var operationErr *vowifiipc.OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != "browser_media_transport_unavailable" {
		t.Fatalf("start call err=%v", err)
	}
}

func TestStatusHeartbeatAdvancesSequence(t *testing.T) {
	backend, err := NewBackend("line-1", "native", "process-1", &fakeFactory{run: &fakeRuntime{}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := backend.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := backend.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Sequence != first.Sequence+1 || second.ObservedAt.Before(first.ObservedAt) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}
