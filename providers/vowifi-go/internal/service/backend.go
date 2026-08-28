// SPDX-License-Identifier: AGPL-3.0-only

// Package service owns one VoWiFi line runtime. It deliberately does not own
// process supervision, container restart, browser heartbeat, or Core recovery.
package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type Runtime interface {
	Layers() Layers
	Close(context.Context) error
}

type Factory interface {
	Start(context.Context) (Runtime, error)
}

type Layers struct {
	Tunnel    vowifiipc.LayerStatus
	IMS       vowifiipc.LayerStatus
	Voice     vowifiipc.LayerStatus
	Messaging vowifiipc.LayerStatus
}

type Backend struct {
	mu sync.Mutex

	lineID, providerID, generation string
	factory                        Factory
	condition                      vowifiipc.RuntimeCondition
	code                           string
	sequence                       uint64
	runtime                        Runtime
	operations                     OperationStore
	media                          MediaDirectory
	callGuardTimeout               time.Duration
	activeCall                     *activeVoiceCall
	drainLease                     string
	messageSends                   int
}

func NewBackend(lineID, providerID, generation string, factory Factory) (*Backend, error) {
	return NewBackendWithStore(lineID, providerID, generation, factory, NewMemoryOperationStore())
}

func NewBackendWithStore(lineID, providerID, generation string, factory Factory, operations OperationStore) (*Backend, error) {
	return NewBackendWithMediaStore(lineID, providerID, generation, factory, operations, nil, defaultCallGuardTimeout)
}

func NewBackendWithMediaStore(lineID, providerID, generation string, factory Factory, operations OperationStore,
	media MediaDirectory, callGuardTimeout time.Duration,
) (*Backend, error) {
	if lineID == "" || providerID == "" || generation == "" || factory == nil {
		return nil, errors.New("invalid VoWiFi service backend configuration")
	}
	if operations == nil {
		return nil, errors.New("VoWiFi service operation store is required")
	}
	drainLease, err := operations.MaintenanceLease()
	if err != nil {
		return nil, errors.Join(errors.New("read VoWiFi maintenance state"), err)
	}
	if drainLease != "" && (vowifiipc.MaintenanceRequest{LeaseID: drainLease}).Validate() != nil {
		return nil, errors.New("stored VoWiFi maintenance lease is invalid")
	}
	if callGuardTimeout <= 0 || callGuardTimeout > time.Minute {
		return nil, errors.New("call guard timeout must be positive and no greater than one minute")
	}
	stopped := stoppedLayers()
	probe := vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: lineID, ProviderID: providerID,
		ProcessGeneration: generation, Sequence: 1, ObservedAt: time.Now().UTC(),
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeStopped},
		Tunnel:  stopped.Tunnel, IMS: stopped.IMS, Voice: stopped.Voice, Messaging: stopped.Messaging,
	}
	if err := probe.Validate(); err != nil {
		return nil, errors.Join(errors.New("invalid VoWiFi service identity"), err)
	}
	return &Backend{
		lineID: lineID, providerID: providerID, generation: generation, factory: factory,
		condition: vowifiipc.RuntimeStopped, sequence: 1, operations: operations,
		media: media, callGuardTimeout: callGuardTimeout, drainLease: drainLease,
	}, nil
}

func (backend *Backend) Status(context.Context) (vowifiipc.Snapshot, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.snapshotLocked(), nil
}

func (backend *Backend) Start(ctx context.Context, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	if err := request.Validate(); err != nil {
		return vowifiipc.OperationResult{}, err
	}
	backend.mu.Lock()
	if result, err, found := backend.replayLocked(request.OperationID, "start"); found || err != nil {
		backend.mu.Unlock()
		return result, err
	}
	if backend.drainLease != "" {
		backend.mu.Unlock()
		return vowifiipc.OperationResult{}, notReady("apply_drain_active", "maintenance")
	}
	if backend.condition == vowifiipc.RuntimeStarting || backend.condition == vowifiipc.RuntimeStopping ||
		backend.condition == vowifiipc.RuntimeRunning {
		backend.mu.Unlock()
		return vowifiipc.OperationResult{}, conflict("runtime_busy")
	}
	if err := backend.operations.Reserve(backend.generation, request.OperationID, "start"); err != nil {
		backend.mu.Unlock()
		return vowifiipc.OperationResult{}, err
	}
	backend.transitionLocked(vowifiipc.RuntimeStarting, "opening_swu")
	backend.mu.Unlock()

	runtime, err := backend.factory.Start(ctx)

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if err != nil || runtime == nil {
		if err == nil {
			err = &StageError{Layer: "runtime", Code: "runtime_missing", Err: errors.New("factory returned nil runtime")}
		}
		failure := publicFailure(err)
		if runtime != nil {
			if closeErr := closeBounded(5*time.Second, runtime.Close); closeErr != nil {
				err = errors.Join(err, closeErr)
				failure = publicFailure(&StageError{Layer: "runtime", Code: "close_failed", Err: err})
			}
		}
		backend.transitionLocked(vowifiipc.RuntimeFailed, failure.Code)
		if storeErr := backend.operations.CompleteFailure(backend.generation, request.OperationID, failure); storeErr != nil {
			return vowifiipc.OperationResult{}, errors.Join(failure, storeErr)
		}
		return vowifiipc.OperationResult{}, failure
	}
	backend.runtime = runtime
	backend.transitionLocked(vowifiipc.RuntimeRunning, "ready")
	result := vowifiipc.OperationResult{
		OperationID: request.OperationID, Accepted: true, Code: "started", Status: backend.snapshotLocked(),
	}
	if err := backend.operations.Complete(backend.generation, request.OperationID, result); err != nil {
		closeErr := closeBounded(5*time.Second, runtime.Close)
		backend.runtime = nil
		backend.transitionLocked(vowifiipc.RuntimeFailed, "operation_store_failed")
		return vowifiipc.OperationResult{}, errors.Join(err, closeErr)
	}
	return result, nil
}

func (backend *Backend) Stop(ctx context.Context, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	if err := request.Validate(); err != nil {
		return vowifiipc.OperationResult{}, err
	}
	backend.mu.Lock()
	if result, err, found := backend.replayLocked(request.OperationID, "stop"); found || err != nil {
		backend.mu.Unlock()
		return result, err
	}
	if backend.condition == vowifiipc.RuntimeStarting || backend.condition == vowifiipc.RuntimeStopping {
		backend.mu.Unlock()
		return vowifiipc.OperationResult{}, conflict("runtime_busy")
	}
	if backend.runtime == nil {
		if err := backend.operations.Reserve(backend.generation, request.OperationID, "stop"); err != nil {
			backend.mu.Unlock()
			return vowifiipc.OperationResult{}, err
		}
		backend.transitionLocked(vowifiipc.RuntimeStopped, "stopped")
		result := vowifiipc.OperationResult{
			OperationID: request.OperationID, Accepted: true, Code: "already_stopped", Status: backend.snapshotLocked(),
		}
		if err := backend.operations.Complete(backend.generation, request.OperationID, result); err != nil {
			backend.mu.Unlock()
			return vowifiipc.OperationResult{}, err
		}
		backend.mu.Unlock()
		return result, nil
	}
	runtime := backend.runtime
	active := backend.activeCall
	if active != nil && active.call == nil {
		backend.mu.Unlock()
		return vowifiipc.OperationResult{}, conflictLayer("call_start_in_progress", "call")
	}
	if err := backend.operations.Reserve(backend.generation, request.OperationID, "stop"); err != nil {
		backend.mu.Unlock()
		return vowifiipc.OperationResult{}, err
	}
	if active != nil {
		active.phase = "ending"
		if active.guardCancel != nil {
			active.guardCancel()
			active.guardCancel = nil
		}
	}
	backend.transitionLocked(vowifiipc.RuntimeStopping, "closing")
	backend.mu.Unlock()

	var err error
	callEnded := false
	failureLayer, stopFailureCode := "runtime", "close_failed"
	if active != nil {
		_, err = active.call.End(ctx)
		if err == nil {
			callEnded = true
			active.session.EndStream("runtime stopped")
		} else {
			failureLayer, stopFailureCode = "call", "call_end_failed"
		}
	}
	if err == nil {
		err = runtime.Close(ctx)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if callEnded && backend.activeCall == active {
		backend.activeCall = nil
		backend.sequence++
	}
	if err != nil {
		backend.transitionLocked(vowifiipc.RuntimeFailed, stopFailureCode)
		failure := publicFailure(&StageError{Layer: failureLayer, Code: stopFailureCode, Err: err})
		if storeErr := backend.operations.CompleteFailure(backend.generation, request.OperationID, failure); storeErr != nil {
			return vowifiipc.OperationResult{}, errors.Join(failure, storeErr)
		}
		return vowifiipc.OperationResult{}, failure
	}
	backend.runtime = nil
	backend.activeCall = nil
	backend.transitionLocked(vowifiipc.RuntimeStopped, "stopped")
	result := vowifiipc.OperationResult{
		OperationID: request.OperationID, Accepted: true, Code: "stopped", Status: backend.snapshotLocked(),
	}
	if err := backend.operations.Complete(backend.generation, request.OperationID, result); err != nil {
		return vowifiipc.OperationResult{}, err
	}
	return result, nil
}

type MessagingRuntime interface {
	Runtime
	SendMessage(context.Context, vowifiipc.SendMessageRequest) error
}

func (backend *Backend) SendMessage(ctx context.Context, request vowifiipc.SendMessageRequest) (vowifiipc.MessageResult, error) {
	if err := request.Validate(); err != nil {
		return vowifiipc.MessageResult{}, err
	}
	kind := operationKind("message_send", request.MessageID, request.Recipient, request.Body)
	backend.mu.Lock()
	if result, err, found := backend.replayMessageLocked(request.OperationID, request.MessageID, kind); found || err != nil {
		backend.mu.Unlock()
		return result, err
	}
	if backend.drainLease != "" {
		backend.mu.Unlock()
		return vowifiipc.MessageResult{}, notReady("apply_drain_active", "maintenance")
	}
	if backend.condition != vowifiipc.RuntimeRunning || backend.runtime == nil {
		backend.mu.Unlock()
		return vowifiipc.MessageResult{}, notReady("runtime_not_running", "runtime")
	}
	messagingRuntime, ok := backend.runtime.(MessagingRuntime)
	if !ok || !backend.runtime.Layers().Messaging.Available {
		backend.mu.Unlock()
		return vowifiipc.MessageResult{}, notReady("messaging_transport_unavailable", "messaging")
	}
	if err := backend.operations.Reserve(backend.generation, request.OperationID, kind); err != nil {
		backend.mu.Unlock()
		return vowifiipc.MessageResult{}, err
	}
	backend.messageSends++
	backend.mu.Unlock()

	sendErr := messagingRuntime.SendMessage(ctx, request)
	backend.mu.Lock()
	backend.messageSends--
	if sendErr != nil {
		failure := publicFailure(&StageError{Layer: "messaging", Code: "message_send_failed", Err: sendErr})
		storeErr := backend.operations.CompleteFailure(backend.generation, request.OperationID, failure)
		backend.mu.Unlock()
		if storeErr != nil {
			return vowifiipc.MessageResult{}, errors.Join(failure, storeErr)
		}
		return vowifiipc.MessageResult{}, failure
	}

	result := vowifiipc.MessageResult{
		OperationResult: vowifiipc.OperationResult{
			OperationID: request.OperationID, Accepted: true, Code: "sent", Status: backend.snapshotLocked(),
		},
		MessageID: request.MessageID,
	}
	storeErr := backend.operations.Complete(backend.generation, request.OperationID, result.OperationResult)
	backend.mu.Unlock()
	if storeErr != nil {
		// The network side effect has already completed. Returning the storage
		// error leaves the durable reservation pending, so it is never resent.
		return vowifiipc.MessageResult{}, storeErr
	}
	return result, nil
}

func (backend *Backend) replayMessageLocked(operationID, messageID, kind string) (vowifiipc.MessageResult, error, bool) {
	result, err, found := backend.replayLocked(operationID, kind)
	if !found || err != nil {
		return vowifiipc.MessageResult{}, err, found
	}
	return vowifiipc.MessageResult{OperationResult: result, MessageID: messageID}, nil, true
}

func (backend *Backend) replayLocked(operationID, kind string) (vowifiipc.OperationResult, error, bool) {
	prior, found, err := backend.operations.Lookup(backend.generation, operationID)
	if err != nil {
		return vowifiipc.OperationResult{}, err, false
	}
	if !found {
		return vowifiipc.OperationResult{}, nil, false
	}
	if prior.Kind != kind {
		return vowifiipc.OperationResult{}, conflict("operation_id_reused"), true
	}
	if !prior.Done {
		return vowifiipc.OperationResult{}, conflict("operation_result_unknown"), true
	}
	if prior.Failure != nil {
		failure := *prior.Failure
		return vowifiipc.OperationResult{}, &failure, true
	}
	return prior.Result, nil, true
}

func (backend *Backend) transitionLocked(condition vowifiipc.RuntimeCondition, code string) {
	backend.condition = condition
	backend.code = code
	backend.sequence++
}

func (backend *Backend) snapshotLocked() vowifiipc.Snapshot {
	backend.sequence++
	layers := stoppedLayers()
	if backend.runtime != nil && backend.condition == vowifiipc.RuntimeRunning {
		layers = backend.runtime.Layers()
	} else if backend.condition == vowifiipc.RuntimeStarting {
		layers.Tunnel = vowifiipc.LayerStatus{Condition: vowifiipc.LayerConnecting, Code: "opening_swu"}
	} else if backend.condition == vowifiipc.RuntimeFailed {
		layers.Tunnel = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: backend.code}
	}
	return vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion,
		LineID:        backend.lineID, ProviderID: backend.providerID, ProcessGeneration: backend.generation,
		Sequence: backend.sequence, ObservedAt: time.Now().UTC(),
		Runtime:   vowifiipc.RuntimeStatus{Condition: backend.condition, Code: backend.code},
		Tunnel:    layers.Tunnel,
		IMS:       layers.IMS,
		Voice:     layers.Voice,
		Messaging: layers.Messaging,
		Maintenance: vowifiipc.MaintenanceStatus{
			Draining: backend.drainLease != "", Code: maintenanceCode(backend.drainLease), LeaseID: backend.drainLease,
		},
		ActiveCall: backend.activeCallSnapshotLocked(),
	}
}

func maintenanceCode(lease string) string {
	if lease != "" {
		return "apply_drain"
	}
	return ""
}

func (backend *Backend) activeCallSnapshotLocked() *vowifiipc.ActiveCall {
	if backend.activeCall == nil {
		return nil
	}
	condition := vowifiipc.CallDialing
	switch backend.activeCall.phase {
	case "active":
		condition = vowifiipc.CallActive
	case "ending":
		condition = vowifiipc.CallEnding
	}
	return &vowifiipc.ActiveCall{CallID: backend.activeCall.request.CallID, Condition: condition}
}

func stoppedLayers() Layers {
	stopped := vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped, Code: "stopped"}
	return Layers{Tunnel: stopped, IMS: stopped, Voice: stopped, Messaging: stopped}
}

type StageError struct {
	Layer string
	Code  string
	Err   error
}

func (failure *StageError) Error() string { return failure.Layer + ": " + failure.Err.Error() }
func (failure *StageError) Unwrap() error { return failure.Err }

func failureCode(err error) string {
	var stage *StageError
	if errors.As(err, &stage) && stage.Code != "" {
		return stage.Code
	}
	return "start_failed"
}

func conflict(code string) error {
	return &vowifiipc.OperationError{Kind: vowifiipc.ErrorConflict, Code: code, Layer: "runtime"}
}

func conflictLayer(code, layer string) error {
	return &vowifiipc.OperationError{Kind: vowifiipc.ErrorConflict, Code: code, Layer: layer}
}

func notReady(code, layer string) error {
	return &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: code, Layer: layer}
}

func publicFailure(err error) *vowifiipc.OperationError {
	var operationErr *vowifiipc.OperationError
	if errors.As(err, &operationErr) {
		copy := *operationErr
		return &copy
	}
	var stage *StageError
	if errors.As(err, &stage) {
		return &vowifiipc.OperationError{
			Kind: vowifiipc.ErrorFailed, Code: stage.Code, Layer: stage.Layer, Detail: diagnosticDetail(stage.Err),
		}
	}
	return &vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "operation_failed"}
}

func diagnosticDetail(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Join(strings.Fields(err.Error()), " ")
	runes := []rune(value)
	if len(runes) > 512 {
		value = string(runes[:512])
	}
	return value
}

var _ vowifiipc.Backend = (*Backend)(nil)
