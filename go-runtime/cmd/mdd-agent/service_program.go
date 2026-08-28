package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

type managedHost struct {
	run              func(context.Context) error
	onUnexpectedExit func(error)

	mu       sync.Mutex
	started  bool
	stopping bool
	cancel   context.CancelFunc
	done     chan error
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
	host.cancel = cancel
	host.done = make(chan error, 1)
	done := host.done
	go func() {
		err := host.run(ctx)
		done <- err
		host.mu.Lock()
		unexpected := !host.stopping
		host.mu.Unlock()
		if unexpected && host.onUnexpectedExit != nil {
			host.onUnexpectedExit(err)
		}
	}()
	return nil
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
	var err error
	select {
	case err = <-done:
		if errors.Is(err, context.Canceled) {
			err = nil
		}
	case <-timer.C:
		return errors.New("service host did not stop before the deadline")
	}
	host.mu.Lock()
	host.started = false
	host.cancel = nil
	host.done = nil
	host.mu.Unlock()
	return err
}

func serviceStopTimeout(settings config) time.Duration {
	timeout := time.Duration(settings.OperationTimeoutSeconds)*time.Second + 5*time.Second
	if timeout > 15*time.Second {
		return 15 * time.Second
	}
	return timeout
}
