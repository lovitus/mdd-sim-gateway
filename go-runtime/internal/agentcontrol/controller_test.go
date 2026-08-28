package agentcontrol

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeWorker struct {
	mu           sync.Mutex
	runs         int
	ready        bool
	exit         chan error
	ignoreCancel bool
}

func (worker *fakeWorker) Run(ctx context.Context, ready func()) error {
	worker.mu.Lock()
	worker.runs++
	shouldReady := worker.ready
	exit := worker.exit
	ignoreCancel := worker.ignoreCancel
	worker.mu.Unlock()
	if shouldReady {
		ready()
	}
	if ignoreCancel {
		return <-exit
	}
	select {
	case err := <-exit:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *fakeWorker) runCount() int {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.runs
}

func TestOneRuntimeAndExplicitStop(t *testing.T) {
	worker := &fakeWorker{ready: true, exit: make(chan error)}
	controller, err := New(worker, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	started, err := controller.Start(context.Background())
	if err != nil || started.State != StateRunning {
		t.Fatalf("started = %+v, error = %v", started, err)
	}
	if _, err := controller.Start(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate start error = %v", err)
	}
	stopContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stopped, err := controller.Stop(stopContext)
	if err != nil || stopped.State != StateStopped {
		t.Fatalf("stopped = %+v, error = %v", stopped, err)
	}
	if worker.runCount() != 1 {
		t.Fatalf("worker runs = %d", worker.runCount())
	}
}

func TestUnexpectedExitIsReportedWithoutRestart(t *testing.T) {
	exit := make(chan error, 1)
	worker := &fakeWorker{ready: true, exit: exit}
	controller, err := New(worker, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	exit <- errors.New("device loop failed")
	waitForState(t, controller, StateFailed)
	time.Sleep(20 * time.Millisecond)
	if worker.runCount() != 1 {
		t.Fatalf("worker was auto-restarted %d times", worker.runCount())
	}
	status := controller.Status()
	if status.Code != "runtime_failed" || status.Detail != "device loop failed" {
		t.Fatalf("status = %+v", status)
	}
}

func TestManualRestartAfterFailureCreatesOneNewGeneration(t *testing.T) {
	exit := make(chan error, 2)
	worker := &fakeWorker{ready: true, exit: exit}
	controller, err := New(worker, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := controller.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exit <- errors.New("first failure")
	waitForState(t, controller, StateFailed)
	second, err := controller.Start(context.Background())
	if err != nil || second.Generation != first.Generation+1 || second.State != StateRunning {
		t.Fatalf("second = %+v, error = %v", second, err)
	}
	if worker.runCount() != 2 {
		t.Fatalf("runs = %d", worker.runCount())
	}
	stopContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := controller.Stop(stopContext); err != nil {
		t.Fatal(err)
	}
}

func TestHungStopStaysStoppingAndBlocksReplacement(t *testing.T) {
	worker := &fakeWorker{ready: true, exit: make(chan error), ignoreCancel: true}
	controller, err := New(worker, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	status, err := controller.Stop(stopContext)
	if !errors.Is(err, context.DeadlineExceeded) || status.State != StateStopping {
		t.Fatalf("stop = %+v, error = %v", status, err)
	}
	if _, err := controller.Start(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement start error = %v", err)
	}
	worker.exit <- nil
	waitForState(t, controller, StateStopped)
}

func waitForState(t *testing.T, controller *Controller, wanted State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if controller.Status().State == wanted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state = %s, want %s", controller.Status().State, wanted)
}
