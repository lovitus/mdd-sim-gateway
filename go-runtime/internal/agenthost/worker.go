// Package agenthost composes the PC/SC monitor and outbound Agent WSS into the
// one hardware runtime owned by agentcontrol.Controller.
package agenthost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type Config struct {
	ServerURL   string
	ServerToken string
	AgentID     string
	HTTPClient  *http.Client
	Monitors    agentreader.MonitorFactory
	Connector   agentsim.Connector
	PINs        map[string]string
	ScanEvery   time.Duration
	Recovery    recovery.Policy
}

type Worker struct{ config Config }

func New(config Config) (*Worker, error) {
	if len(config.ServerToken) < 32 || config.HTTPClient == nil || config.Monitors == nil || config.Connector == nil || config.ScanEvery <= 0 {
		return nil, errors.New("invalid Agent host configuration")
	}
	if err := (agentlink.Hello{SchemaVersion: agentlink.SchemaVersion, AgentID: config.AgentID, ProcessGeneration: "validation"}).Validate(); err != nil {
		return nil, err
	}
	if _, err := config.Recovery.Decide(recovery.Failure{Attempt: 1, Recoverable: true}); err != nil {
		return nil, err
	}
	config.PINs = copyPINs(config.PINs)
	return &Worker{config: config}, nil
}

func (worker *Worker) Run(ctx context.Context, ready func()) error {
	generation, err := randomGeneration()
	if err != nil {
		return err
	}
	manager, err := agentsim.NewManager(worker.config.Connector, agentsim.PINResolverFunc(func(_ context.Context, cardID string) (string, error) {
		return worker.config.PINs[cardID], nil
	}))
	if err != nil {
		return err
	}
	reader := agentreader.Worker{
		Monitors: worker.config.Monitors, Sessions: manager, ScanInterval: worker.config.ScanEvery,
		Recovery: worker.config.Recovery,
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	readerReady := make(chan struct{}, 1)
	readerDone := make(chan error, 1)
	linkDone := make(chan error, 1)
	go func() { readerDone <- reader.Run(runContext, func() { readerReady <- struct{}{} }) }()
	go func() { linkDone <- worker.runAgentLink(runContext, manager, generation) }()

	localReady := false
	for {
		select {
		case <-readerReady:
			if !localReady {
				localReady = true
				ready()
			}
		case readerErr := <-readerDone:
			cancel()
			<-linkDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return readerErr
		case linkErr := <-linkDone:
			cancel()
			<-readerDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return linkErr
		case <-ctx.Done():
			cancel()
			<-readerDone
			<-linkDone
			return ctx.Err()
		}
	}
}

func (worker *Worker) runAgentLink(ctx context.Context, authenticator agentlink.Authenticator, generation string) error {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var connected atomic.Bool
		err := (agentlink.Client{
			URL: worker.config.ServerURL, Token: worker.config.ServerToken,
			Hello:      agentlink.Hello{SchemaVersion: agentlink.SchemaVersion, AgentID: worker.config.AgentID, ProcessGeneration: generation},
			HTTPClient: worker.config.HTTPClient, Authenticator: authenticator, OperationTimeout: 30 * time.Second,
			Connected: func() { connected.Store(true) },
		}).Run(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected.Load() {
			attempt = 0
		}
		attempt++
		decision, policyErr := worker.config.Recovery.Decide(recovery.Failure{
			Attempt: attempt, Recoverable: true, Action: recovery.ActionReconnect,
		})
		if policyErr != nil || !decision.Retry {
			return errors.Join(err, policyErr)
		}
		timer := time.NewTimer(decision.After)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func copyPINs(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for cardID, pin := range input {
		result[cardID] = pin
	}
	return result
}

func randomGeneration() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
