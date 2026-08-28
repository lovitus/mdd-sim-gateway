// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/service"
)

type messageReporter struct {
	outbox service.MessageOutbox
	client providermessages.Client
	wake   chan struct{}
}

func newMessageReporter(settings config, generation string, outbox service.MessageOutbox) (*messageReporter, error) {
	if outbox == nil || settings.Core.RegistrationURL == "" {
		return nil, nil
	}
	parsed, err := url.Parse(settings.Core.RegistrationURL)
	if err != nil {
		return nil, err
	}
	parsed.Path, parsed.RawPath = "/v1/provider/messages", ""
	client := providermessages.Client{URL: parsed.String(), Token: settings.Core.RegistrationToken}
	if err := client.Validate(); err != nil {
		return nil, err
	}
	if err := outbox.AdoptMessages(settings.LineID, settings.ProviderID, generation); err != nil {
		return nil, err
	}
	return &messageReporter{outbox: outbox, client: client, wake: make(chan struct{}, 1)}, nil
}

func (reporter *messageReporter) Publish(event providermessages.Event) error {
	if reporter == nil {
		return errors.New("provider message reporter is unavailable")
	}
	if err := reporter.outbox.EnqueueMessage(event); err != nil {
		return err
	}
	select {
	case reporter.wake <- struct{}{}:
	default:
	}
	return nil
}

func (reporter *messageReporter) maintain(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-reporter.wake:
		}
		reporter.flush(ctx)
	}
}

func (reporter *messageReporter) flush(parent context.Context) {
	for {
		events, err := reporter.outbox.PendingMessages(32)
		if err != nil || len(events) == 0 {
			return
		}
		for _, event := range events {
			ctx, cancel := context.WithTimeout(parent, 3*time.Second)
			err := reporter.client.Report(ctx, event)
			cancel()
			if err != nil {
				return
			}
			if err := reporter.outbox.DeleteMessage(event); err != nil {
				return
			}
		}
	}
}

var _ service.MessageSink = (*messageReporter)(nil)
