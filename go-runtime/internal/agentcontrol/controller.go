// Package agentcontrol owns the one Agent runtime shared by service, CLI, and
// GUI frontends. A runtime exit is reported; it is never auto-restarted here.
package agentcontrol

import (
	"context"
	"errors"
	"sync"
	"time"
)

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

var (
	ErrConflict    = errors.New("agent runtime transition conflicts with current state")
	ErrStartFailed = errors.New("agent runtime exited before becoming ready")
)

type Worker interface {
	// Run blocks until the runtime stops. It must call ready exactly when local
	// device ownership and required background loops are established.
	Run(ctx context.Context, ready func()) error
}

type Snapshot struct {
	State      State     `json:"state"`
	Generation uint64    `json:"generation"`
	ChangedAt  time.Time `json:"changed_at"`
	Code       string    `json:"code,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

type Controller struct {
	mu       sync.Mutex
	worker   Worker
	now      func() time.Time
	snapshot Snapshot
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(worker Worker, now func() time.Time) (*Controller, error) {
	if worker == nil {
		return nil, errors.New("agent worker is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Controller{worker: worker, now: now, snapshot: Snapshot{
		State: StateStopped, ChangedAt: now().UTC(),
	}}, nil
}

func (controller *Controller) Status() Snapshot {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.snapshot
}

func (controller *Controller) Start(ctx context.Context) (Snapshot, error) {
	controller.mu.Lock()
	if controller.snapshot.State == StateStarting || controller.snapshot.State == StateRunning ||
		controller.snapshot.State == StateStopping {
		snapshot := controller.snapshot
		controller.mu.Unlock()
		return snapshot, ErrConflict
	}
	runContext, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	var readyOnce sync.Once
	controller.cancel = cancel
	controller.done = done
	controller.snapshot = Snapshot{
		State: StateStarting, Generation: controller.snapshot.Generation + 1,
		ChangedAt: controller.now().UTC(), Code: "runtime_starting",
	}
	generation := controller.snapshot.Generation
	controller.mu.Unlock()

	go controller.run(runContext, generation, done, func() { readyOnce.Do(func() { close(ready) }) })
	select {
	case <-ready:
		controller.mu.Lock()
		if controller.snapshot.Generation == generation && controller.snapshot.State == StateStarting {
			controller.snapshot.State = StateRunning
			controller.snapshot.ChangedAt = controller.now().UTC()
			controller.snapshot.Code = "runtime_running"
		}
		snapshot := controller.snapshot
		controller.mu.Unlock()
		if snapshot.State != StateRunning {
			return snapshot, ErrStartFailed
		}
		return snapshot, nil
	case <-done:
		return controller.Status(), ErrStartFailed
	case <-ctx.Done():
		controller.mu.Lock()
		if controller.snapshot.Generation == generation && controller.snapshot.State == StateStarting {
			controller.snapshot.State = StateStopping
			controller.snapshot.ChangedAt = controller.now().UTC()
			controller.snapshot.Code = "start_cancelled_waiting_for_runtime"
		}
		controller.mu.Unlock()
		cancel()
		return controller.Status(), ctx.Err()
	}
}

func (controller *Controller) Stop(ctx context.Context) (Snapshot, error) {
	controller.mu.Lock()
	switch controller.snapshot.State {
	case StateStopped:
		snapshot := controller.snapshot
		controller.mu.Unlock()
		return snapshot, nil
	case StateFailed:
		controller.snapshot.State = StateStopped
		controller.snapshot.ChangedAt = controller.now().UTC()
		controller.snapshot.Code = "runtime_stopped"
		controller.snapshot.Detail = ""
		snapshot := controller.snapshot
		controller.mu.Unlock()
		return snapshot, nil
	case StateStopping:
		// Continue waiting for the one existing shutdown below.
	default:
		controller.snapshot.State = StateStopping
		controller.snapshot.ChangedAt = controller.now().UTC()
		controller.snapshot.Code = "runtime_stopping"
		controller.cancel()
	}
	done := controller.done
	controller.mu.Unlock()

	select {
	case <-done:
		return controller.Status(), nil
	case <-ctx.Done():
		return controller.Status(), ctx.Err()
	}
}

func (controller *Controller) run(ctx context.Context, generation uint64, done chan struct{}, ready func()) {
	err := controller.worker.Run(ctx, ready)
	controller.mu.Lock()
	if controller.snapshot.Generation == generation {
		if controller.snapshot.State == StateStopping {
			controller.snapshot = Snapshot{
				State: StateStopped, Generation: generation,
				ChangedAt: controller.now().UTC(), Code: "runtime_stopped",
			}
		} else {
			code := "runtime_exited"
			if err != nil {
				code = "runtime_failed"
			}
			controller.snapshot = Snapshot{
				State: StateFailed, Generation: generation,
				ChangedAt: controller.now().UTC(), Code: code,
			}
			if err != nil {
				controller.snapshot.Detail = err.Error()
			}
		}
		controller.cancel = nil
	}
	controller.mu.Unlock()
	close(done)
}
