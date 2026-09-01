package notifications

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errNotificationConfigChanged = errors.New("notification configuration changed")

type EventVerifier interface {
	ValidateNotificationEvent(context.Context, Event) string
}

type EngineConfig struct {
	Context  context.Context
	Store    *Store
	Sender   Sender
	Verifier EventVerifier
	Now      func() time.Time
}

type Engine struct {
	store    *Store
	sender   Sender
	verifier EventVerifier
	now      func() time.Time
	ctx      context.Context
	cancel   context.CancelFunc
	wake     chan struct{}

	mu       sync.Mutex
	inflight map[string]context.CancelCauseFunc
	started  bool
	wait     sync.WaitGroup
}

func NewEngine(config EngineConfig) (*Engine, error) {
	if config.Context == nil || config.Store == nil || config.Sender == nil {
		return nil, errors.New("invalid notification engine configuration")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	ctx, cancel := context.WithCancel(config.Context)
	return &Engine{store: config.Store, sender: config.Sender, verifier: config.Verifier,
		now: config.Now, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		inflight: map[string]context.CancelCauseFunc{}}, nil
}

func (engine *Engine) BindVerifier(verifier EventVerifier) error {
	if engine == nil || verifier == nil {
		return errors.New("notification event verifier is required")
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.verifier != nil {
		return errors.New("notification event verifier is already bound")
	}
	engine.verifier = verifier
	return nil
}

func (engine *Engine) Start() error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.verifier == nil {
		return errors.New("notification engine started without event verifier")
	}
	if engine.started {
		return nil
	}
	engine.started = true
	for _, channel := range []string{ChannelWebhook, ChannelTelegram, ChannelPushPlus} {
		engine.wait.Add(1)
		go engine.runChannel(channel)
	}
	return nil
}

func (engine *Engine) Close() error {
	engine.cancel()
	engine.mu.Lock()
	for _, cancel := range engine.inflight {
		cancel(context.Canceled)
	}
	engine.mu.Unlock()
	engine.wait.Wait()
	return nil
}

func (engine *Engine) Wake() {
	select {
	case engine.wake <- struct{}{}:
	default:
	}
}

func (engine *Engine) ConfigChanged() {
	engine.mu.Lock()
	for _, cancel := range engine.inflight {
		cancel(errNotificationConfigChanged)
	}
	engine.mu.Unlock()
	engine.Wake()
}

func (engine *Engine) runChannel(channel string) {
	defer engine.wait.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if engine.ctx.Err() != nil {
			return
		}
		processed, err := engine.processOne(channel)
		if err == nil && processed {
			continue
		}
		select {
		case <-engine.ctx.Done():
			return
		case <-engine.wake:
		case <-ticker.C:
		}
	}
}

func (engine *Engine) processOne(channel string) (bool, error) {
	now := engine.now().UTC()
	pending, found, err := engine.store.Pending(channel, now)
	if err != nil || !found {
		return false, err
	}
	if event, _, eventErr := engine.store.EventForDelivery(pending.DeliveryID); eventErr != nil {
		return false, eventErr
	} else if code := engine.verifier.ValidateNotificationEvent(engine.ctx, event); code != "" {
		_, cancelErr := engine.store.Cancel(pending.DeliveryID, code, now)
		return true, cancelErr
	}
	// Register the cancelable request before claiming. Otherwise a config
	// update can commit after Claim but before this worker becomes visible in
	// inflight, allowing one stale request to escape cancellation.
	requestContext, cancel := context.WithCancelCause(engine.ctx)
	engine.mu.Lock()
	engine.inflight[channel] = cancel
	engine.mu.Unlock()
	delivery, event, config, claimed, err := engine.store.Claim(pending.DeliveryID, now)
	if err != nil || !claimed {
		cancel(nil)
		engine.mu.Lock()
		delete(engine.inflight, channel)
		engine.mu.Unlock()
		return false, err
	}
	if code := engine.verifier.ValidateNotificationEvent(requestContext, event); code != "" {
		cancel(nil)
		engine.mu.Lock()
		delete(engine.inflight, channel)
		engine.mu.Unlock()
		_, completeErr := engine.store.Complete(delivery.DeliveryID, DeliveryCanceled, code, 0, time.Time{}, engine.now().UTC())
		return true, completeErr
	}
	outcome := engine.sender.Send(requestContext, delivery, event, config)
	cause := context.Cause(requestContext)
	cancel(nil)
	engine.mu.Lock()
	delete(engine.inflight, channel)
	engine.mu.Unlock()
	finished := engine.now().UTC()
	if errors.Is(cause, errNotificationConfigChanged) {
		if outcome.Wrote {
			outcome = Outcome{State: DeliveryUncertain, Code: "notification_config_changed_after_write", Wrote: true}
		} else {
			outcome = Outcome{State: DeliveryCanceled, Code: "notification_config_changed_before_write"}
		}
	} else if errors.Is(cause, context.Canceled) && !outcome.Wrote {
		outcome.Retryable = true
		outcome.Code = "notification_shutdown_before_write"
	}
	if outcome.Retryable && !outcome.Wrote && delivery.Attempts < 3 {
		delay := 2 * time.Second
		if delivery.Attempts >= 2 {
			delay = 5 * time.Second
		}
		_, err = engine.store.Complete(delivery.DeliveryID, DeliveryPending, "", 0, finished.Add(delay), finished)
		return true, err
	}
	if outcome.Retryable && !outcome.Wrote {
		outcome.State, outcome.Code = DeliveryFailed, "notification_prewrite_attempts_exhausted"
	}
	if !oneOf(outcome.State, DeliveryDelivered, DeliveryFailed, DeliveryUncertain, DeliveryCanceled) {
		outcome.State, outcome.Code = DeliveryUncertain, "notification_outcome_invalid"
	}
	_, err = engine.store.Complete(delivery.DeliveryID, outcome.State, outcome.Code,
		outcome.HTTPStatus, time.Time{}, finished)
	return true, err
}
