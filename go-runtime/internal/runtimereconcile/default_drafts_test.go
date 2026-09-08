package runtimereconcile

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultDraftWorkerIsSingleFlightAndCancelledOnClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var calls atomic.Int32
	reconciler := &Reconciler{ctx: ctx, cancel: cancel, now: time.Now, logf: func(string, ...any) {},
		reconcileDefaultDrafts: func(ctx context.Context) error { calls.Add(1); close(started); <-ctx.Done(); return ctx.Err() }}
	returned := make(chan struct{})
	go func() { reconciler.scheduleDefaultDrafts(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("default setup blocked scheduling")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	for range 10 {
		reconciler.scheduleDefaultDrafts()
	}
	reconciler.Close()
	if calls.Load() != 1 {
		t.Fatal("duplicate workers", calls.Load())
	}
}
