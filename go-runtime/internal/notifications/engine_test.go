package notifications

import (
	"context"
	"sync"
	"testing"
	"time"
)

type verifierFunc func(context.Context, Event) string

func (function verifierFunc) ValidateNotificationEvent(ctx context.Context, event Event) string {
	return function(ctx, event)
}

type senderFunc func(context.Context, Delivery, Event, Config) Outcome

func (function senderFunc) Send(ctx context.Context, delivery Delivery, event Event, config Config) Outcome {
	return function(ctx, delivery, event, config)
}

func TestEngineConfigChangeCancelsPrewriteAndMarksPostwriteUncertain(t *testing.T) {
	for _, test := range []struct {
		name  string
		wrote bool
		state string
		code  string
	}{
		{name: "prewrite", state: DeliveryCanceled, code: "notification_config_changed_before_write"},
		{name: "postwrite", wrote: true, state: DeliveryUncertain, code: "notification_config_changed_after_write"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openNotificationStore(t)
			now := time.Unix(1_800_000_000, 0).UTC()
			enableWebhook(t, store, now)
			_, deliveries, _, err := store.Intake(Event{
				SourceID: "engine-source-" + test.name, Type: EventIncomingSMS, LineID: "line-1", CardID: "12345678",
				Transport: "vowifi", Title: "SMS", Text: "hello", Peer: "+100", OccurredAt: now,
			}, now)
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			var once sync.Once
			engine, err := NewEngine(EngineConfig{
				Context: t.Context(), Store: store, Now: func() time.Time { return now.Add(time.Second) },
				Verifier: verifierFunc(func(context.Context, Event) string { return "" }),
				Sender: senderFunc(func(ctx context.Context, _ Delivery, _ Event, _ Config) Outcome {
					once.Do(func() { close(started) })
					<-ctx.Done()
					return Outcome{State: DeliveryDelivered, Code: "notification_delivered", Wrote: test.wrote}
				}),
			})
			if err != nil || engine.Start() != nil {
				t.Fatalf("engine err=%v", err)
			}
			defer engine.Close()
			engine.Wake()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("sender did not start")
			}
			config, _ := store.Config()
			config.Webhook.Enabled = false
			if _, changed, err := store.PutConfigExpected(config.Revision, config, now.Add(2*time.Second)); err != nil || !changed {
				t.Fatalf("config changed=%t err=%v", changed, err)
			}
			engine.ConfigChanged()
			deadline := time.Now().Add(2 * time.Second)
			for {
				history, err := store.Deliveries(10)
				if err != nil {
					t.Fatal(err)
				}
				if len(history) == 1 && history[0].State == test.state {
					if history[0].Code != test.code || history[0].DeliveryID != deliveries[0].DeliveryID {
						t.Fatalf("history=%+v", history)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("history=%+v", history)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
