// Package agentreader reconciles transient reader/card attachments with one
// cancellable session per present card. Reader names are attachment keys, not
// durable SIM identities; EID/ICCID/profile identity belongs above this layer.
package agentreader

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type Reader struct {
	Name              string
	CardPresent       bool
	SessionGeneration string
	ATR               []byte
}

type MonitorCondition string

const (
	MonitorStarting   MonitorCondition = "starting"
	MonitorReady      MonitorCondition = "ready"
	MonitorRecovering MonitorCondition = "recovering"
)

type Observation struct {
	Condition MonitorCondition
	Detail    string
	Readers   []Reader
}

type Monitor interface {
	Scan(ctx context.Context) ([]Reader, error)
	Wait(ctx context.Context, current []Reader, maximum time.Duration) error
	Close() error
}

type MonitorFactory interface {
	Open(ctx context.Context) (Monitor, error)
}

type SessionRunner interface {
	Run(ctx context.Context, reader Reader) error
}

type PermanentError interface {
	Permanent() bool
}

type RetryAfterError interface {
	RetryAfter() time.Duration
}

type Worker struct {
	Monitors     MonitorFactory
	Sessions     SessionRunner
	ScanInterval time.Duration
	Recovery     recovery.Policy
	Observed     func(Observation)
}

type activeSession struct {
	generation string
	cancel     context.CancelFunc
}

func (worker Worker) Run(ctx context.Context, ready func()) error {
	if worker.Monitors == nil || worker.Sessions == nil || worker.ScanInterval <= 0 {
		return errors.New("invalid reader worker configuration")
	}
	if _, err := worker.Recovery.Decide(recovery.Failure{Attempt: 1, Recoverable: true}); err != nil {
		return fmt.Errorf("invalid reader recovery policy: %w", err)
	}
	var readyOnce sync.Once
	attempt := 0
	worker.observe(Observation{Condition: MonitorStarting})
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		monitor, err := worker.Monitors.Open(ctx)
		if err == nil && monitor == nil {
			err = errors.New("monitor factory returned nil")
		}
		if err == nil {
			err = worker.runMonitor(ctx, monitor, func() { readyOnce.Do(ready) })
			closeErr := monitor.Close()
			err = errors.Join(err, closeErr)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		worker.observe(Observation{Condition: MonitorRecovering, Detail: err.Error()})
		attempt++
		if isPermanent(err) {
			return err
		}
		if err := waitForRetry(ctx, worker.Recovery, attempt, err); err != nil {
			return err
		}
	}
}

func (worker Worker) runMonitor(ctx context.Context, monitor Monitor, ready func()) error {
	current, err := monitor.Scan(ctx)
	if err != nil {
		return err
	}
	active := make(map[string]activeSession)
	var sessions sync.WaitGroup
	defer func() {
		for _, session := range active {
			session.cancel()
		}
		sessions.Wait()
	}()
	if err := worker.reconcile(ctx, current, active, &sessions); err != nil {
		return err
	}
	worker.observe(Observation{Condition: MonitorReady, Readers: current})
	ready()
	for {
		if err := monitor.Wait(ctx, current, worker.ScanInterval); err != nil {
			return err
		}
		current, err = monitor.Scan(ctx)
		if err != nil {
			return err
		}
		if err := worker.reconcile(ctx, current, active, &sessions); err != nil {
			return err
		}
		worker.observe(Observation{Condition: MonitorReady, Readers: current})
	}
}

func (worker Worker) observe(observation Observation) {
	if worker.Observed == nil {
		return
	}
	snapshot := make([]Reader, len(observation.Readers))
	for index, reader := range observation.Readers {
		snapshot[index] = reader
		snapshot[index].ATR = append([]byte(nil), reader.ATR...)
	}
	observation.Readers = snapshot
	worker.Observed(observation)
}

func (worker Worker) reconcile(ctx context.Context, readers []Reader,
	active map[string]activeSession, sessions *sync.WaitGroup) error {
	discovered := make(map[string]Reader, len(readers))
	for _, reader := range readers {
		if reader.Name == "" {
			return errors.New("reader attachment name is empty")
		}
		if _, duplicate := discovered[reader.Name]; duplicate {
			return fmt.Errorf("duplicate reader attachment %q", reader.Name)
		}
		if reader.CardPresent && reader.SessionGeneration == "" {
			return fmt.Errorf("present card in %q has no session generation", reader.Name)
		}
		reader.ATR = append([]byte(nil), reader.ATR...)
		discovered[reader.Name] = reader
	}
	for name, session := range active {
		reader, exists := discovered[name]
		if !exists || !reader.CardPresent || reader.SessionGeneration != session.generation {
			session.cancel()
			delete(active, name)
		}
	}
	for name, reader := range discovered {
		if !reader.CardPresent {
			continue
		}
		if _, exists := active[name]; exists {
			continue
		}
		sessionContext, cancel := context.WithCancel(ctx)
		active[name] = activeSession{generation: reader.SessionGeneration, cancel: cancel}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			worker.runSession(sessionContext, reader)
		}()
	}
	return nil
}

func (worker Worker) runSession(ctx context.Context, reader Reader) {
	attempt := 0
	for {
		err := worker.Sessions.Run(ctx, reader)
		if ctx.Err() != nil || isPermanent(err) {
			return
		}
		attempt++
		if err := waitForRetry(ctx, worker.Recovery, attempt, err); err != nil {
			return
		}
	}
}

func waitForRetry(ctx context.Context, policy recovery.Policy, attempt int, failure error) error {
	var permanent PermanentError
	recoverable := !errors.As(failure, &permanent) || !permanent.Permanent()
	var providerDelay *time.Duration
	var delayed RetryAfterError
	if errors.As(failure, &delayed) {
		delay := delayed.RetryAfter()
		providerDelay = &delay
	}
	decision, err := policy.Decide(recovery.Failure{
		Attempt: attempt, Recoverable: recoverable, ProviderDelay: providerDelay,
		Action: recovery.ActionRetry,
	})
	if err != nil {
		return err
	}
	if !decision.Retry {
		return failure
	}
	timer := time.NewTimer(decision.After)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isPermanent(err error) bool {
	var permanent PermanentError
	return errors.As(err, &permanent) && permanent.Permanent()
}
