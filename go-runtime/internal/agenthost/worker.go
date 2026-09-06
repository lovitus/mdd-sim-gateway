// Package agenthost composes the PC/SC monitor and outbound Agent WSS into the
// one hardware runtime owned by agentcontrol.Controller.
package agenthost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentevents"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsms"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawcapture"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type Config struct {
	ServerURL       string
	ServerToken     string
	AgentID         string
	HTTPClient      *http.Client
	Monitors        agentreader.MonitorFactory
	Connector       agentsim.Connector
	Modems          agentmodem.Prober
	Operations      agentmodem.ManagedOperator
	Media           agentmodem.MediaOperator
	Data            agentdata.Backend
	ModemSIMs       agentmodem.SIMAuthenticator
	ModemPINRuntime agentmodem.SIMPINRuntime
	ModemAuxiliary  agentmodem.AuxiliaryCoordinator
	// Provision is optional until the platform-specific hardware transaction
	// executor is available. A nil value intentionally remains fail-closed.
	Provision             agentlink.ProvisionExecutor
	ProvisionHardware     ProvisionHardware
	ModemEvents           *agentevents.Store
	ModemPolicies         *agentpolicy.Manager
	ModemEventOperator    agentmodem.Operator
	ModemEventCoordinator agentmodem.BackgroundScanCoordinator
	RawUSBSource          agentrawusb.SourceBackend
	RawUSBImportGuard     agentrawusb.ImportGuard
	RawCapture            *rawcapture.Controller
	ModemPINs             agentmodem.PINRecoverer
	EUICCDownloads        *agentsim.DownloadStore
	PINs                  map[string]string
	ScanEvery             time.Duration
	Recovery              recovery.Policy
}

type Worker struct {
	config       Config
	mu           sync.RWMutex
	topology     *topologyState
	modems       *modemTopologyState
	manager      *agentsim.Manager
	modemCycleMu sync.Mutex
	staleAfter   time.Duration
	eventScanner *agentevents.Scanner
}

func New(config Config) (*Worker, error) {
	if len(config.ServerToken) < 32 || config.HTTPClient == nil || config.Monitors == nil || config.Connector == nil || config.ScanEvery <= 0 {
		return nil, errors.New("invalid Agent host configuration")
	}
	if config.Operations != nil && config.Modems == nil {
		return nil, errors.New("modem operations require the matching topology prober")
	}
	if config.Media != nil && config.Modems == nil {
		return nil, errors.New("modem media requires the matching topology prober")
	}
	if config.Data != nil && config.Modems == nil {
		return nil, errors.New("modem data requires the matching topology prober")
	}
	if config.Data != nil && (config.ModemAuxiliary == nil || config.ModemPolicies == nil) {
		return nil, errors.New("modem data requires paid-call and persistent policy coordination")
	}
	if config.ModemSIMs != nil && (config.ModemAuxiliary == nil || config.Modems == nil) {
		return nil, errors.New("modem SIM AKA requires matching topology and paid-call coordination")
	}
	if (config.RawUSBSource != nil || config.RawUSBImportGuard != nil) && (config.Modems == nil || config.ModemAuxiliary == nil) {
		return nil, errors.New("raw USB modem mode requires matching modem topology and paid-call coordination")
	}
	if config.RawCapture != nil && (config.Modems == nil || config.RawUSBSource != config.RawCapture) {
		return nil, errors.New("local raw capture controller must be the configured source owner")
	}
	if config.ModemPINs != nil && config.Modems == nil {
		return nil, errors.New("modem SIM PIN recovery requires matching topology")
	}
	if config.ModemPINRuntime != nil && (config.Modems == nil || config.ModemAuxiliary == nil) {
		return nil, errors.New("modem SIM PIN actions require matching topology and paid-call coordination")
	}
	if (config.ModemEvents != nil || config.ModemEventOperator != nil || config.ModemEventCoordinator != nil) &&
		(config.ModemEvents == nil || config.ModemEventOperator == nil || config.ModemEventCoordinator == nil || config.Modems == nil) {
		return nil, errors.New("modem events require matching store, operator, coordinator, and topology")
	}
	if config.ModemPolicies != nil && config.Modems == nil {
		return nil, errors.New("modem policies require matching topology")
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
	modems, err := newModemTopologyState()
	if err != nil {
		return nil, fmt.Errorf("initialize modem SIM generations: %w", err)
	}
	if config.Modems == nil {
		modems.observe(agentmodem.Observation{Condition: agentmodem.ConditionDisabled})
	}
	worker := &Worker{config: config, topology: &topologyState{}, modems: modems, staleAfter: staleAfter}
	if config.ModemEvents != nil {
		eventEvery := config.ScanEvery
		if eventEvery < 2*time.Second {
			eventEvery = 2 * time.Second
		}
		worker.eventScanner, err = agentevents.NewScanner(agentevents.ScannerConfig{
			Store: config.ModemEvents, Operator: config.ModemEventOperator,
			Coordinator: config.ModemEventCoordinator, Topology: worker.Topology, Every: eventEvery,
		})
		if err != nil {
			return nil, err
		}
	}
	return worker, nil
}

func (worker *Worker) Close() error {
	var errorsSeen []error
	if worker.config.RawCapture != nil {
		errorsSeen = append(errorsSeen, worker.config.RawCapture.Shutdown())
	}
	if worker.config.EUICCDownloads != nil {
		errorsSeen = append(errorsSeen, worker.config.EUICCDownloads.Close())
	}
	if closer, ok := worker.config.ModemPINs.(interface{ Close() error }); ok {
		errorsSeen = append(errorsSeen, closer.Close())
	}
	if closer, ok := worker.config.Operations.(interface{ Close() error }); ok {
		errorsSeen = append(errorsSeen, closer.Close())
	}
	if closer, ok := worker.config.Modems.(interface{ Close() error }); ok {
		errorsSeen = append(errorsSeen, closer.Close())
	}
	if worker.config.ModemEvents != nil {
		errorsSeen = append(errorsSeen, worker.config.ModemEvents.Close())
	}
	if worker.config.ModemPolicies != nil {
		errorsSeen = append(errorsSeen, worker.config.ModemPolicies.Close())
	}
	return errors.Join(errorsSeen...)
}

func (worker *Worker) Run(ctx context.Context, ready func()) error {
	generation, err := randomGeneration()
	if err != nil {
		return err
	}
	manager, err := agentsim.NewManagerWithDownloadStore(worker.config.Connector, agentsim.PINResolverFunc(func(_ context.Context, cardID string) (string, error) {
		return worker.config.PINs[cardID], nil
	}), worker.config.EUICCDownloads)
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
	operationDone := make(chan error, 1)
	captureDone := make(chan error, 1)
	linkDone := make(chan error, 1)
	go func() { readerDone <- reader.Run(runContext, func() { readerReady <- struct{}{} }) }()
	go func() { modemDone <- worker.runModemWorkers(runContext) }()
	if worker.config.Operations == nil {
		go func() { <-runContext.Done(); operationDone <- runContext.Err() }()
	} else {
		go func() { operationDone <- worker.config.Operations.Run(runContext) }()
	}
	if worker.config.RawCapture == nil {
		go func() { <-runContext.Done(); captureDone <- runContext.Err() }()
	} else {
		go func() { captureDone <- worker.config.RawCapture.Run(runContext) }()
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
			<-operationDone
			<-captureDone
			<-linkDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return readerErr
		case linkErr := <-linkDone:
			cancel()
			<-readerDone
			<-modemDone
			<-operationDone
			<-captureDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return linkErr
		case modemErr := <-modemDone:
			cancel()
			<-readerDone
			<-operationDone
			<-captureDone
			<-linkDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return modemErr
		case operationErr := <-operationDone:
			cancel()
			<-readerDone
			<-modemDone
			<-captureDone
			<-linkDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return operationErr
		case captureErr := <-captureDone:
			cancel()
			<-readerDone
			<-modemDone
			<-operationDone
			<-linkDone
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return captureErr
		case <-ctx.Done():
			cancel()
			<-readerDone
			<-modemDone
			<-operationDone
			<-captureDone
			<-linkDone
			return ctx.Err()
		}
	}
}

func (worker *Worker) runModemWorkers(ctx context.Context) error {
	if worker.config.Modems == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	if worker.eventScanner == nil {
		return (agentmodem.Worker{
			Prober: worker.config.Modems, PINs: worker.config.ModemPINs,
			Policies: worker.config.ModemPolicies, Interval: worker.config.ScanEvery,
			Recovery: worker.config.Recovery, Observed: worker.modems.observe,
			Coordinator: modemCycleCoordinator{mu: &worker.modemCycleMu},
		}).Run(ctx)
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	topologyDone := make(chan error, 1)
	eventDone := make(chan error, 1)
	go func() {
		topologyDone <- (agentmodem.Worker{
			Prober: worker.config.Modems, PINs: worker.config.ModemPINs,
			Policies: worker.config.ModemPolicies, Interval: worker.config.ScanEvery,
			Recovery: worker.config.Recovery, Observed: worker.modems.observe,
			Coordinator: modemCycleCoordinator{mu: &worker.modemCycleMu},
		}).Run(runContext)
	}()
	go func() { eventDone <- worker.eventScanner.Run(runContext) }()
	select {
	case err := <-topologyDone:
		cancel()
		<-eventDone
		return err
	case err := <-eventDone:
		cancel()
		<-topologyDone
		return err
	}
}

func (worker *Worker) runAgentLink(ctx context.Context, manager *agentsim.Manager, generation string) error {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var media *agentmedia.Manager
		var data *agentdata.Manager
		var rawUSB *agentrawusb.Manager
		if worker.config.Media != nil {
			var err error
			media, err = agentmedia.NewManager(agentmedia.Config{
				Context: ctx, ServerURL: worker.config.ServerURL, ServerToken: worker.config.ServerToken,
				AgentID: worker.config.AgentID, ProcessGeneration: generation,
				HTTPClient: worker.config.HTTPClient, Endpoints: worker.config.Media,
			})
			if err != nil {
				return err
			}
		}
		if worker.config.Data != nil {
			var err error
			data, err = agentdata.NewManager(agentdata.Config{
				Context: ctx, ServerURL: worker.config.ServerURL, ServerToken: worker.config.ServerToken,
				AgentID: worker.config.AgentID, ProcessGeneration: generation,
				HTTPClient: worker.config.HTTPClient, Backend: worker.config.Data,
				Coordinator: worker.config.ModemAuxiliary, Admission: worker.config.ModemPolicies,
			})
			if err != nil {
				if media != nil {
					_ = media.Close()
				}
				return err
			}
			if worker.config.ModemPolicies != nil {
				if err := worker.config.ModemPolicies.BindCoordinator(composeAuxiliary(data, worker.config.ModemAuxiliary)); err != nil {
					_ = data.Close()
					if media != nil {
						_ = media.Close()
					}
					return err
				}
			}
		}
		if worker.config.RawUSBSource != nil || worker.config.RawUSBImportGuard != nil {
			var err error
			rawUSB, err = agentrawusb.NewManager(agentrawusb.Config{
				Context: ctx, ServerURL: worker.config.ServerURL, ServerToken: worker.config.ServerToken,
				AgentID: worker.config.AgentID, ProcessGeneration: generation,
				HTTPClient: worker.config.HTTPClient, Topology: worker.Topology,
				Source: worker.config.RawUSBSource, Coordinator: composeAuxiliary(data, worker.config.ModemAuxiliary),
				ImportGuard: worker.config.RawUSBImportGuard,
			})
			if err != nil {
				if data != nil {
					_ = data.Close()
				}
				if media != nil {
					_ = media.Close()
				}
				return err
			}
		}
		var connected atomic.Bool
		health := worker.Topology
		if rawUSB != nil {
			health = func() agentlink.TopologySnapshot { return rawUSB.Topology(worker.Topology()) }
		}
		modems := agentlink.ModemExecutor(worker)
		authenticator := agentlink.Authenticator(worker)
		var modemEvents agentlink.ModemEventSource
		if worker.config.ModemEvents != nil {
			modemEvents = worker.config.ModemEvents
		}
		var policyExecutor agentlink.ModemPolicyExecutor
		if worker.config.ModemPolicies != nil {
			policyExecutor = worker
		}
		var pinExecutor agentlink.SIMPINExecutor
		if worker.config.ModemPINRuntime != nil {
			pinExecutor = worker
		}
		var dataExecutor agentlink.ModemDataExecutor
		if data != nil {
			dataExecutor = data
		}
		provision := worker.config.Provision
		if provision == nil && worker.config.ProvisionHardware != nil {
			provision = worker
		}
		if data != nil {
			modems = dataCoordinatedModems{worker: worker, data: data}
			authenticator = dataCoordinatedAuthenticator{worker: worker, data: data}
		}
		err := (agentlink.Client{
			URL: worker.config.ServerURL, Token: worker.config.ServerToken,
			Hello:      agentlink.Hello{SchemaVersion: agentlink.SchemaVersion, AgentID: worker.config.AgentID, ProcessGeneration: generation},
			HTTPClient: worker.config.HTTPClient, Authenticator: authenticator, Modems: modems, Media: media,
			Data: dataExecutor, Policies: policyExecutor, RawUSB: rawUSB, EUICC: manager,
			ReaderReadback: manager,
			PIN:            pinExecutor,
			Provision:      provision,
			Downloads:      manager, Discovery: manager, Notifications: manager,
			Events:           modemEvents,
			OperationTimeout: 30 * time.Second,
			Connected:        func() { connected.Store(true) }, Health: health,
		}).Run(ctx)
		if rawUSB != nil {
			_ = rawUSB.Close()
		}
		if media != nil {
			_ = media.Close()
		}
		if data != nil {
			_ = data.Close()
			if worker.config.ModemPolicies != nil {
				_ = worker.config.ModemPolicies.BindCoordinator(worker.config.ModemAuxiliary)
			}
		}
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

func (worker *Worker) AuthenticateAKA(ctx context.Context, request agentlink.AKARequest) agentlink.AKAResponse {
	response := agentlink.AKAResponse{OperationID: request.OperationID, SessionGeneration: request.SessionGeneration}
	if err := request.Validate(); err != nil {
		response.Failure = &agentlink.RemoteError{Kind: "rejected", Code: "invalid_aka_request"}
		return response
	}
	kind := request.DeviceKind
	if kind == "" || kind == agentlink.AKADeviceReader {
		worker.mu.RLock()
		manager := worker.manager
		worker.mu.RUnlock()
		if manager == nil {
			response.Failure = &agentlink.RemoteError{Kind: "not_ready", Code: "card_session_replaced", Retryable: true}
			return response
		}
		return manager.AuthenticateAKA(ctx, request)
	}

	worker.mu.RLock()
	sims := worker.config.ModemSIMs
	coordinator := worker.config.ModemAuxiliary
	worker.mu.RUnlock()
	if sims == nil || coordinator == nil || !worker.matchesModemSIMRequest(request) {
		response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_sim_session_replaced"}
		return response
	}
	var result agentmodem.SIMAKAResult
	err := coordinator.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
		var operationErr error
		result, operationErr = sims.AuthenticateSIMAKA(operationContext, agentmodem.SIMAKARequest{
			AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID, CardID: request.CardID,
			Application: string(request.Application), RAND: append([]byte(nil), request.RAND...),
			AUTN: append([]byte(nil), request.AUTN...),
		})
		return operationErr
	})
	if err != nil {
		switch {
		case errors.Is(err, agentcall.ErrAuxiliaryDuringCall):
			response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_paid_call_active"}
		case errors.Is(err, agentmodem.ErrOperationTargetReplaced):
			response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_sim_session_replaced"}
		case errors.Is(err, agentmodem.ErrOperationUnavailable):
			response.Failure = &agentlink.RemoteError{Kind: "not_ready", Code: "modem_sim_aka_unavailable", Retryable: true}
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			response.Failure = &agentlink.RemoteError{Kind: "transport", Code: "modem_sim_aka_timeout", Retryable: true}
		default:
			response.Failure = &agentlink.RemoteError{Kind: "transport", Code: "modem_sim_aka_failed", Retryable: true}
		}
		return response
	}
	response.Body, response.SW1, response.SW2 = append([]byte(nil), result.Body...), result.SW1, result.SW2
	return response
}

func (worker *Worker) matchesModemSIMRequest(request agentlink.AKARequest) bool {
	condition, _, modems := worker.modems.snapshot()
	if condition != agentlink.ModemReady {
		return false
	}
	matches := 0
	for _, modem := range modems {
		if modem.AttachmentID == request.AttachmentID && modem.EquipmentID == request.EquipmentID &&
			modem.SIM.ICCID == request.CardID && modem.SIM.SessionGeneration == request.SessionGeneration &&
			modem.AT.State == "ready" && modem.AT.SIMAPDU {
			matches++
		}
	}
	return matches == 1
}

// ExecuteSIMPIN currently exposes the safe modem PIN1 entry primitive. PIN
// change and enable/disable remain explicitly unavailable until the underlying
// adapter has typed operations for them; they must not fall back to raw AT.
func (worker *Worker) ExecuteSIMPIN(ctx context.Context, request agentlink.SIMPINRequest) agentlink.SIMPINResponse {
	response := agentlink.SIMPINResponse{OperationID: request.OperationID, CardID: request.CardID,
		ReaderName: request.ReaderName, AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID,
		SIMSessionGeneration: request.SIMSessionGeneration, Action: request.Action, State: "unavailable"}
	if request.Action == "" {
		response.Failure = &agentlink.RemoteError{Kind: "not_ready", Code: "sim_pin_action_unavailable"}
		return response
	}
	if request.ReaderName != "" {
		worker.mu.RLock()
		manager := worker.manager
		worker.mu.RUnlock()
		if manager == nil {
			response.Failure = &agentlink.RemoteError{Kind: "not_ready", Code: "sim_pin_reader_unavailable", Retryable: true}
			return response
		}
		return manager.ExecuteSIMPIN(ctx, request)
	}
	if request.Action != agentlink.SIMPINVerify || worker.config.ModemPINRuntime == nil {
		response.Failure = &agentlink.RemoteError{Kind: "not_ready", Code: "sim_pin_modem_unavailable", Retryable: true}
		return response
	}
	condition, _, modems := worker.modems.snapshot()
	if agentmodem.Condition(condition) != agentmodem.ConditionReady {
		response.Failure = &agentlink.RemoteError{Kind: "not_ready", Code: "modem_sim_unavailable", Retryable: true}
		return response
	}
	matches := 0
	for _, modem := range modems {
		if modem.AttachmentID == request.AttachmentID && modem.EquipmentID == request.EquipmentID &&
			modem.SIM.ICCID == request.CardID && modem.SIM.SessionGeneration == request.SIMSessionGeneration &&
			modem.SIM.State == "ready" {
			matches++
		}
	}
	if matches != 1 {
		response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_sim_session_replaced"}
		return response
	}
	var result agentmodem.SIMPINResult
	err := worker.config.ModemAuxiliary.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
		var operationErr error
		result, operationErr = worker.config.ModemPINRuntime.EnterSIMPIN(operationContext, agentmodem.SIMPINRequest{
			AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID, CardID: request.CardID, PIN: request.PIN,
		})
		return operationErr
	})
	response.State = "verified"
	if result.AttemptsRemaining != nil {
		remaining := *result.AttemptsRemaining
		response.AttemptsRemaining = &remaining
	}
	if err != nil || !result.Ready {
		response.State = "failed"
		code := "sim_pin_verify_failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "sim_pin_operation_timeout"
		}
		response.Failure = &agentlink.RemoteError{Kind: "failed", Code: code, Retryable: err != nil}
		return response
	}
	return response
}

func (worker *Worker) ExecuteModem(ctx context.Context, request agentlink.ModemRequest) agentlink.ModemResponse {
	response := agentlink.ModemResponse{
		OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
	}
	worker.mu.RLock()
	running := worker.manager != nil
	operator := worker.config.Operations
	supported := operator != nil
	worker.mu.RUnlock()
	if !running || !supported {
		response.Failure = &agentlink.RemoteError{
			Kind: "not_ready", Code: "modem_operations_unavailable", Retryable: true,
		}
		return response
	}
	action := agentmodem.OperationAction(request.Action)
	result, err := operator.Operate(ctx, agentmodem.Operation{
		OperationID:  request.OperationID,
		AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, Action: action, LeaseID: request.LeaseID, Number: request.Number,
		Signal: request.Signal, Body: request.Body,
		IncomingEventID: request.IncomingEventID, SIMSessionGeneration: request.SIMSessionGeneration,
		NativeCallIndex: request.NativeCallIndex, CallOccurrence: request.CallOccurrence,
	})
	if err != nil {
		switch {
		case errors.Is(err, agentmodem.ErrOperationTargetReplaced):
			response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_target_replaced"}
		case errors.Is(err, agentmodem.ErrOperationUnavailable):
			response.Failure = &agentlink.RemoteError{Kind: "not_ready", Code: "modem_at_unavailable", Retryable: true}
		case errors.Is(err, agentmodem.ErrIncomingCallChanged):
			response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_incoming_call_changed"}
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			response.Failure = &agentlink.RemoteError{Kind: "transport", Code: "modem_operation_timeout", Retryable: true}
		case errors.Is(err, agentcall.ErrLeaseConflict), errors.Is(err, agentcall.ErrLeaseMismatch),
			errors.Is(err, agentcall.ErrLeaseExpired):
			response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_call_lease_conflict"}
		case errors.Is(err, agentcall.ErrLeaseNotFound):
			response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_call_lease_not_found"}
		case errors.Is(err, agentcall.ErrAuxiliaryDuringCall):
			response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_paid_call_active"}
		case errors.Is(err, agentsms.ErrConflict):
			response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_sms_operation_conflict"}
		case errors.Is(err, agentsms.ErrSubmitUncertain):
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_sms_submit_uncertain"}
		case request.Action == agentlink.ModemCallHangup:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_hangup_unconfirmed", Retryable: true}
		case request.Action == agentlink.ModemCallDial || request.Action == agentlink.ModemCallAnswer:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_call_start_uncertain", Retryable: true}
		case request.Action == agentlink.ModemCallReject:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_incoming_reject_uncertain"}
		case request.Action == agentlink.ModemCallRenew:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_call_renew_failed", Retryable: true}
		case request.Action == agentlink.ModemCallDTMF:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_dtmf_failed", Retryable: true}
		case request.Action == agentlink.ModemSMSSend:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_sms_submit_failed", Retryable: true}
		case request.Action == agentlink.ModemSMSList:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_sms_list_failed", Retryable: true}
		default:
			response.Failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_status_failed", Retryable: true}
		}
		return response
	}
	if request.Action == agentlink.ModemSMSList || request.Action == agentlink.ModemSMSSend {
		messages := make([]agentlink.ModemSMSMessage, 0, len(result.SMS.Messages))
		for _, message := range result.SMS.Messages {
			messages = append(messages, agentlink.ModemSMSMessage{
				Index: message.Index, State: message.State, Direction: message.Direction,
				Peer: message.Peer, Body: message.Body, ObservedAt: message.ObservedAt,
				Fingerprint: message.Fingerprint, Reference: message.Reference, Delivery: message.Delivery,
			})
		}
		response.SMS = &agentlink.ModemSMSResult{
			State: result.SMS.State, Messages: messages, References: append([]int(nil), result.SMS.References...),
		}
	} else if request.Action != agentlink.ModemCallRenew {
		response.Call = &agentlink.ModemCallResult{
			State: result.Call.State, Direction: result.Call.Direction, Number: result.Call.Number,
			NativeCallIndex: result.Call.NativeIndex, VoiceCalls: result.Call.VoiceCalls,
			IncomingCalls: result.Call.IncomingCalls,
			ObservedAt:    result.Call.ObservedAt, Authoritative: result.Call.Authoritative,
			TerminalConfirmed: result.Call.TerminalConfirmed, Strategy: result.Call.Strategy,
		}
	}
	if result.LeaseID != "" {
		response.Lease = &agentlink.ModemLeaseResult{LeaseID: result.LeaseID, ExpiresAt: result.LeaseUntil}
	}
	return response
}

func (worker *Worker) ExecuteModemPolicy(ctx context.Context, request agentlink.ModemPolicyRequest) agentlink.ModemPolicyResponse {
	response := agentlink.ModemPolicyResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
		SIMSessionGeneration: request.SIMSessionGeneration}
	if worker.config.ModemPolicies == nil {
		response.Failure = &agentlink.RemoteError{Kind: "not_ready", Code: "modem_policy_unavailable"}
		return response
	}
	matches := 0
	for _, modem := range worker.Topology().Modems {
		if modem.AttachmentID == request.AttachmentID && modem.EquipmentID == request.EquipmentID &&
			modem.SIM.ICCID == request.CardID && modem.SIM.SessionGeneration == request.SIMSessionGeneration &&
			modem.SIM.State == "ready" {
			matches++
		}
	}
	if matches != 1 {
		response.Failure = &agentlink.RemoteError{Kind: "conflict", Code: "modem_policy_target_replaced"}
		return response
	}
	return worker.config.ModemPolicies.Execute(ctx, request)
}

type composedAuxiliary struct {
	data *agentdata.Manager
	call agentmodem.AuxiliaryCoordinator
}

func composeAuxiliary(data *agentdata.Manager, call agentmodem.AuxiliaryCoordinator) agentmodem.AuxiliaryCoordinator {
	if data == nil {
		return call
	}
	return composedAuxiliary{data: data, call: call}
}

func (coordinator composedAuxiliary) DoAuxiliary(ctx context.Context, equipmentID string, callback func(context.Context) error) error {
	return coordinator.data.DoAuxiliary(ctx, equipmentID, func(dataContext context.Context) error {
		return coordinator.call.DoAuxiliary(dataContext, equipmentID, callback)
	})
}

type modemCycleCoordinator struct{ mu *sync.Mutex }

func (coordinator modemCycleCoordinator) DoBackgroundScan(ctx context.Context, callback func(context.Context) error) error {
	if coordinator.mu == nil || callback == nil {
		return errors.New("invalid modem cycle coordinator")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return callback(ctx)
}

type dataCoordinatedModems struct {
	worker *Worker
	data   *agentdata.Manager
}

func (executor dataCoordinatedModems) ExecuteModem(ctx context.Context, request agentlink.ModemRequest) agentlink.ModemResponse {
	switch request.Action {
	case agentlink.ModemCallDial, agentlink.ModemCallAnswer, agentlink.ModemSMSList, agentlink.ModemSMSSend:
		var response agentlink.ModemResponse
		err := executor.data.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
			response = executor.worker.ExecuteModem(operationContext, request)
			return nil
		})
		if err != nil {
			response = agentlink.ModemResponse{
				OperationID: request.OperationID, AttachmentID: request.AttachmentID,
				EquipmentID: request.EquipmentID, CardID: request.CardID,
				Failure: auxiliaryDataFailure(err),
			}
		}
		return response
	default:
		// Physical hangup, status and lease renewal are deliberately never
		// blocked by data admission. A stale safety lease must retain its one
		// independent path to stop billing even if another state is inconsistent.
		return executor.worker.ExecuteModem(ctx, request)
	}
}

type dataCoordinatedAuthenticator struct {
	worker *Worker
	data   *agentdata.Manager
}

func (authenticator dataCoordinatedAuthenticator) AuthenticateAKA(ctx context.Context, request agentlink.AKARequest) agentlink.AKAResponse {
	if request.DeviceKind == "" || request.DeviceKind == agentlink.AKADeviceReader {
		return authenticator.worker.AuthenticateAKA(ctx, request)
	}
	var response agentlink.AKAResponse
	err := authenticator.data.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
		response = authenticator.worker.AuthenticateAKA(operationContext, request)
		return nil
	})
	if err != nil {
		response = agentlink.AKAResponse{OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			Failure: auxiliaryDataFailure(err)}
	}
	return response
}

func auxiliaryDataFailure(err error) *agentlink.RemoteError {
	if errors.Is(err, agentdata.ErrSessionActive) {
		return &agentlink.RemoteError{Kind: "conflict", Code: "modem_data_session_active", Retryable: true}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &agentlink.RemoteError{Kind: "transport", Code: "modem_auxiliary_timeout", Retryable: true}
	}
	return &agentlink.RemoteError{Kind: "failed", Code: "modem_auxiliary_failed", Retryable: true}
}

// Topology returns the same typed snapshot used by the outbound Agent WSS.
// It never preserves attachments after this Worker generation has stopped.
func (worker *Worker) Topology() agentlink.TopologySnapshot {
	worker.mu.RLock()
	manager := worker.manager
	worker.mu.RUnlock()
	if manager == nil {
		modemCondition, modemDetail, modems := worker.modems.snapshot()
		topology := agentlink.TopologySnapshot{
			ReaderCondition: agentlink.ReaderStarting, Readers: []agentlink.ReaderFact{},
			ModemCondition: modemCondition, ModemDetail: modemDetail, Modems: modems,
		}
		if worker.config.RawCapture != nil {
			topology = worker.config.RawCapture.Topology(topology)
		}
		return worker.withPolicyFacts(topology)
	}
	topology := worker.topology.snapshot(manager.Sessions(), worker.staleAfter)
	topology.ModemCondition, topology.ModemDetail, topology.Modems = worker.modems.snapshot()
	if worker.config.RawCapture != nil {
		topology = worker.config.RawCapture.Topology(topology)
	}
	return worker.withPolicyFacts(topology)
}

func (worker *Worker) withPolicyFacts(topology agentlink.TopologySnapshot) agentlink.TopologySnapshot {
	if worker.config.ModemPolicies == nil {
		return topology
	}
	for index := range topology.Modems {
		modem := &topology.Modems[index]
		if modem.EquipmentID == "" || modem.SIM.ICCID == "" {
			continue
		}
		policy := worker.config.ModemPolicies.View(modem.EquipmentID, modem.SIM.ICCID)
		modem.Policy = &policy
	}
	return topology
}

func (worker *Worker) RawModeSnapshot() (rawcapture.Snapshot, error) {
	if worker.config.RawCapture == nil {
		return rawcapture.Snapshot{}, errors.New("raw mode is unavailable")
	}
	return worker.config.RawCapture.Snapshot()
}

func (worker *Worker) SetRawMode(pair rawcapture.Pair, raw bool) error {
	if worker.config.RawCapture == nil {
		return errors.New("raw mode is unavailable")
	}
	if raw {
		return worker.config.RawCapture.SetRaw(pair)
	}
	return worker.config.RawCapture.SetAdapted(pair)
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
