// Package agenthost composes the PC/SC monitor and outbound Agent WSS into the
// one hardware runtime owned by agentcontrol.Controller.
package agenthost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
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
	Modems      agentmodem.Prober
	PINs        map[string]string
	ScanEvery   time.Duration
	Recovery    recovery.Policy
}

type Worker struct {
	config     Config
	mu         sync.RWMutex
	topology   *topologyState
	modems     *modemTopologyState
	manager    *agentsim.Manager
	staleAfter time.Duration
}

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
	staleAfter := config.ScanEvery * 3
	if staleAfter < time.Second {
		staleAfter = time.Second
	}
	modems := &modemTopologyState{}
	if config.Modems == nil {
		modems.observe(agentmodem.Observation{Condition: agentmodem.ConditionDisabled})
	}
	return &Worker{config: config, topology: &topologyState{}, modems: modems, staleAfter: staleAfter}, nil
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
	worker.topology.observe(agentreader.Observation{Condition: agentreader.MonitorStarting})
	worker.mu.Lock()
	worker.manager = manager
	worker.mu.Unlock()
	defer func() {
		worker.mu.Lock()
		if worker.manager == manager {
			worker.manager = nil
			worker.topology.observe(agentreader.Observation{Condition: agentreader.MonitorStarting})
			condition := agentmodem.ConditionStarting
			if worker.config.Modems == nil {
				condition = agentmodem.ConditionDisabled
			}
			worker.modems.observe(agentmodem.Observation{Condition: condition})
		}
		worker.mu.Unlock()
	}()
	reader := agentreader.Worker{
		Monitors: worker.config.Monitors, Sessions: manager, ScanInterval: worker.config.ScanEvery,
		Recovery: worker.config.Recovery,
	}
	reader.Observed = worker.topology.observe
	reader.SessionFailed = worker.topology.sessionFailed
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	readerReady := make(chan struct{}, 1)
	readerDone := make(chan error, 1)
	modemDone := make(chan error, 1)
	linkDone := make(chan error, 1)
	go func() { readerDone <- reader.Run(runContext, func() { readerReady <- struct{}{} }) }()
	if worker.config.Modems == nil {
		go func() { <-runContext.Done(); modemDone <- runContext.Err() }()
	} else {
		go func() {
			modemDone <- (agentmodem.Worker{
				Prober: worker.config.Modems, Interval: worker.config.ScanEvery,
				Recovery: worker.config.Recovery, Observed: worker.modems.observe,
			}).Run(runContext)
		}()
	}
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
			<-modemDone
			<-linkDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return readerErr
		case linkErr := <-linkDone:
			cancel()
			<-readerDone
			<-modemDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return linkErr
		case modemErr := <-modemDone:
			cancel()
			<-readerDone
			<-linkDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return modemErr
		case <-ctx.Done():
			cancel()
			<-readerDone
			<-modemDone
			<-linkDone
			return ctx.Err()
		}
	}
}

func (worker *Worker) runAgentLink(ctx context.Context, manager *agentsim.Manager, generation string) error {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var connected atomic.Bool
		err := (agentlink.Client{
			URL: worker.config.ServerURL, Token: worker.config.ServerToken,
			Hello:      agentlink.Hello{SchemaVersion: agentlink.SchemaVersion, AgentID: worker.config.AgentID, ProcessGeneration: generation},
			HTTPClient: worker.config.HTTPClient, Authenticator: manager, Modems: worker,
			OperationTimeout: 30 * time.Second,
			Connected:        func() { connected.Store(true) }, Health: worker.Topology,
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
		log.Printf("mdd-agent: Core WSS disconnected: %v; retrying in %s", err, decision.After)
		timer := time.NewTimer(decision.After)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (worker *Worker) ExecuteModem(ctx context.Context, request agentlink.ModemRequest) agentlink.ModemResponse {
	response := agentlink.ModemResponse{
		OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
	}
	worker.mu.RLock()
	running := worker.manager != nil
	operator, supported := worker.config.Modems.(agentmodem.Operator)
	worker.mu.RUnlock()
	if !running || !supported {
		response.Failure = &agentlink.RemoteError{
			Kind: "not_ready", Code: "modem_operations_unavailable", Retryable: true,
		}
		return response
	}
	action := agentmodem.OperationAction(request.Action)
	result, err := operator.Operate(ctx, agentmodem.Operation{
		AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, Action: action,
	})
	if err != nil {
		switch {
		case errors.Is(err, agentmodem.ErrOperationTargetReplaced):
			response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_target_replaced"}
		case errors.Is(err, agentmodem.ErrOperationUnavailable):
			response.Failure = &agentlink.RemoteError{Kind: "not_ready", Code: "modem_at_unavailable", Retryable: true}
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			response.Failure = &agentlink.RemoteError{Kind: "transport", Code: "modem_operation_timeout", Retryable: true}
		case request.Action == agentlink.ModemCallHangup:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_hangup_unconfirmed", Retryable: true}
		default:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_status_failed", Retryable: true}
		}
		return response
	}
	response.Call = &agentlink.ModemCallResult{
		State: result.Call.State, Direction: result.Call.Direction, Number: result.Call.Number,
		ObservedAt: result.Call.ObservedAt, Authoritative: result.Call.Authoritative,
		TerminalConfirmed: result.Call.TerminalConfirmed, Strategy: result.Call.Strategy,
	}
	return response
}

// Topology returns the same typed snapshot used by the outbound Agent WSS.
// It never preserves attachments after this Worker generation has stopped.
func (worker *Worker) Topology() agentlink.TopologySnapshot {
	worker.mu.RLock()
	manager := worker.manager
	worker.mu.RUnlock()
	if manager == nil {
		modemCondition, modemDetail, modems := worker.modems.snapshot()
		return agentlink.TopologySnapshot{
			ReaderCondition: agentlink.ReaderStarting, Readers: []agentlink.ReaderFact{},
			ModemCondition: modemCondition, ModemDetail: modemDetail, Modems: modems,
		}
	}
	topology := worker.topology.snapshot(manager.Sessions(), worker.staleAfter)
	topology.ModemCondition, topology.ModemDetail, topology.Modems = worker.modems.snapshot()
	return topology
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
