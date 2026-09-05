package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

const startupSettleDelay = 10 * time.Millisecond

type managedHost struct {
	run              func(context.Context) error
	onUnexpectedExit func(error)

	mu       sync.Mutex
	started  bool
	stopping bool
	finished bool
	ready    bool
	cancel   context.CancelFunc
	done     chan struct{}
	runErr   error
}

func newManagedHost(run func(context.Context) error, onUnexpectedExit func(error)) (*managedHost, error) {
	if run == nil {
		return nil, errors.New("service host runner is required")
	}
	return &managedHost{run: run, onUnexpectedExit: onUnexpectedExit}, nil
}

func (host *managedHost) start() error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.started {
		return errors.New("service host is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	host.started = true
	host.stopping = false
	host.finished = false
	host.ready = false
	host.runErr = nil
	host.cancel = cancel
	host.done = make(chan struct{})
	done := host.done
	go func() {
		err := host.run(ctx)
		host.mu.Lock()
		if err == nil && !host.stopping {
			err = errors.New("service host exited unexpectedly")
		}
		host.runErr = err
		host.finished = true
		unexpected := !host.stopping
		close(done)
		host.mu.Unlock()
		if unexpected && host.onUnexpectedExit != nil {
			host.onUnexpectedExit(err)
		}
	}()
	return nil
}

// waitReady prevents an OS service or GUI from reporting ownership before the
// shared loopback singleton has actually been bound. A concurrent host exit
// wins over readiness and is returned exactly once; it is never restarted.
func (host *managedHost) waitReady(ready <-chan struct{}, timeout time.Duration) error {
	if ready == nil || timeout <= 0 {
		return errors.New("service host readiness wait is invalid")
	}
	host.mu.Lock()
	if !host.started {
		host.mu.Unlock()
		return errors.New("service host is not running")
	}
	done := host.done
	host.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ready:
		// A runner can signal readiness and return an error in the same scheduling
		// window. Give its completion path a bounded settle barrier before accepting
		// readiness, so an immediate startup failure cannot win or lose by chance.
		settle := time.NewTimer(startupSettleDelay)
		select {
		case <-done:
			settle.Stop()
			return host.complete(done)
		case <-settle.C:
		}
		host.mu.Lock()
		if host.done != done || !host.started {
			host.mu.Unlock()
			return errors.New("service host changed during startup")
		}
		if host.finished {
			host.mu.Unlock()
			return host.complete(done)
		}
		host.ready = true
		host.mu.Unlock()
		return nil
	case <-done:
		return host.complete(done)
	case <-timer.C:
		return errors.New("service host did not become ready before the deadline")
	}
}

func (host *managedHost) readyAccepted() bool {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.started && host.ready
}

func (host *managedHost) stop(timeout time.Duration) error {
	host.mu.Lock()
	if !host.started {
		host.mu.Unlock()
		return nil
	}
	host.stopping = true
	cancel := host.cancel
	done := host.done
	host.mu.Unlock()

	cancel()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		return errors.New("service host did not stop before the deadline")
	}
	return host.complete(done)
}

func (host *managedHost) complete(done chan struct{}) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.done != done || !host.started {
		return nil
	}
	err := host.runErr
	host.started = false
	host.stopping = false
	host.finished = false
	host.ready = false
	host.cancel = nil
	host.done = nil
	host.runErr = nil
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func serviceStopTimeout(settings config) time.Duration {
	timeout := time.Duration(settings.OperationTimeoutSeconds)*time.Second + 5*time.Second
	if timeout > 15*time.Second {
		return 15 * time.Second
	}
	return timeout
}
