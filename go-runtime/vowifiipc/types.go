// Package vowifiipc defines the versioned local control contract between MDD
// Core and one long-lived VoWiFi provider process. It intentionally contains
// no SWu packet, RTP, RTCP, PCM, SIM secret, or host-network payload type.
package vowifiipc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = 2

type RuntimeCondition string

const (
	RuntimeStopped  RuntimeCondition = "stopped"
	RuntimeStarting RuntimeCondition = "starting"
	RuntimeRunning  RuntimeCondition = "running"
	RuntimeStopping RuntimeCondition = "stopping"
	RuntimeFailed   RuntimeCondition = "failed"
)

type LayerCondition string

const (
	LayerUnknown    LayerCondition = "unknown"
	LayerConnecting LayerCondition = "connecting"
	LayerReady      LayerCondition = "ready"
	LayerDegraded   LayerCondition = "degraded"
	LayerBlocked    LayerCondition = "blocked"
	LayerStopped    LayerCondition = "stopped"
)

type CallCondition string

const (
	CallDialing CallCondition = "dialing"
	CallActive  CallCondition = "active"
	CallEnding  CallCondition = "ending"
)

type RuntimeStatus struct {
	Condition RuntimeCondition `json:"condition"`
	Code      string           `json:"code,omitempty"`
}

type LayerStatus struct {
	Condition LayerCondition `json:"condition"`
	Available bool           `json:"available"`
	Code      string         `json:"code,omitempty"`
}

type ActiveCall struct {
	CallID    string        `json:"call_id"`
	Condition CallCondition `json:"condition"`
}

type MaintenanceStatus struct {
	Draining bool   `json:"draining"`
	Code     string `json:"code,omitempty"`
}

// Snapshot is one provider-owned observation. Core assigns its durable epoch
// and receive time after accepting this source/generation/sequence tuple.
type Snapshot struct {
	SchemaVersion     int               `json:"schema_version"`
	LineID            string            `json:"line_id"`
	ProviderID        string            `json:"provider_id"`
	ProcessGeneration string            `json:"process_generation"`
	Sequence          uint64            `json:"sequence"`
	ObservedAt        time.Time         `json:"observed_at"`
	Runtime           RuntimeStatus     `json:"runtime"`
	Tunnel            LayerStatus       `json:"tunnel"`
	IMS               LayerStatus       `json:"ims"`
	Voice             LayerStatus       `json:"voice"`
	Messaging         LayerStatus       `json:"messaging"`
	Maintenance       MaintenanceStatus `json:"maintenance"`
	ActiveCall        *ActiveCall       `json:"active_call,omitempty"`
}

type LifecycleRequest struct {
	OperationID string `json:"operation_id"`
}

type StartCallRequest struct {
	OperationID   string `json:"operation_id"`
	CallID        string `json:"call_id"`
	Callee        string `json:"callee"`
	MediaBufferMS int    `json:"media_buffer_ms"`
}

type EndCallRequest struct {
	OperationID string `json:"operation_id"`
	CallID      string `json:"call_id"`
	ReasonCode  string `json:"reason_code"`
}

type SendMessageRequest struct {
	OperationID string `json:"operation_id"`
	MessageID   string `json:"message_id"`
	Recipient   string `json:"recipient"`
	Body        string `json:"body"`
}

type MaintenanceRequest struct {
	LeaseID string `json:"lease_id"`
}

type OperationResult struct {
	OperationID string   `json:"operation_id"`
	Accepted    bool     `json:"accepted"`
	Code        string   `json:"code,omitempty"`
	Status      Snapshot `json:"status"`
}

type CallResult struct {
	OperationResult
	CallID string `json:"call_id"`
}

type MessageResult struct {
	OperationResult
	MessageID string `json:"message_id"`
}

type MaintenanceResult struct {
	LeaseID  string   `json:"lease_id"`
	Draining bool     `json:"draining"`
	Status   Snapshot `json:"status"`
}

type ErrorKind string

const (
	ErrorInvalid  ErrorKind = "invalid"
	ErrorConflict ErrorKind = "conflict"
	ErrorNotReady ErrorKind = "not_ready"
	ErrorNotFound ErrorKind = "not_found"
	ErrorRejected ErrorKind = "rejected"
	ErrorFailed   ErrorKind = "failed"
)

// OperationError contains only provider-approved diagnostic detail. The
// provider must not place credentials, AKA material, or message bodies here.
type OperationError struct {
	Kind         ErrorKind     `json:"kind"`
	Code         string        `json:"code"`
	Layer        string        `json:"layer,omitempty"`
	Detail       string        `json:"detail,omitempty"`
	RetryAfter   time.Duration `json:"-"`
	RetryAfterMS int64         `json:"retry_after_ms,omitempty"`
}

func (failure *OperationError) Error() string {
	if failure == nil {
		return "VoWiFi operation failed"
	}
	if failure.Detail != "" {
		return fmt.Sprintf("VoWiFi operation failed (%s/%s): %s", failure.Kind, failure.Code, failure.Detail)
	}
	return fmt.Sprintf("VoWiFi operation failed (%s/%s)", failure.Kind, failure.Code)
}

// Backend owns real side effects. Every mutating method MUST persist
// OperationID as an idempotency key before performing an action: an exact
// retry returns the original result, while reuse with different input is a
// conflict. CallID and MessageID are durable business identities, not display
// labels. The IPC layer validates and preserves these identities but cannot
// infer whether a provider-side paid action occurred after a process crash.
type Backend interface {
	Status(context.Context) (Snapshot, error)
	Start(context.Context, LifecycleRequest) (OperationResult, error)
	Stop(context.Context, LifecycleRequest) (OperationResult, error)
	StartCall(context.Context, StartCallRequest) (CallResult, error)
	EndCall(context.Context, EndCallRequest) (CallResult, error)
	SendMessage(context.Context, SendMessageRequest) (MessageResult, error)
}

// MaintenanceBackend is deliberately narrower than Backend so old providers
// fail closed instead of silently accepting an apply drain they do not own.
type MaintenanceBackend interface {
	BeginDrain(context.Context, MaintenanceRequest) (MaintenanceResult, error)
	EndDrain(context.Context, MaintenanceRequest) (MaintenanceResult, error)
}

func (result OperationResult) Validate() error {
	if err := validateOperationID(result.OperationID); err != nil {
		return err
	}
	if !result.Accepted || !validCode(result.Code) {
		return errors.New("operation result is not accepted or has an invalid code")
	}
	return result.Status.Validate()
}

func (result CallResult) Validate() error {
	if err := result.OperationResult.Validate(); err != nil {
		return err
	}
	if !validIdentifier(result.CallID) {
		return errors.New("call result has an invalid call_id")
	}
	return nil
}

func (result MessageResult) Validate() error {
	if err := result.OperationResult.Validate(); err != nil {
		return err
	}
	if !validIdentifier(result.MessageID) {
		return errors.New("message result has an invalid message_id")
	}
	return nil
}

func (result MaintenanceResult) Validate() error {
	if err := validateOperationID(result.LeaseID); err != nil {
		return err
	}
	if err := result.Status.Validate(); err != nil {
		return err
	}
	if result.Draining != result.Status.Maintenance.Draining {
		return errors.New("maintenance result does not match provider status")
	}
	return nil
}

func (snapshot Snapshot) Validate() error {
	if snapshot.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported snapshot schema version %d", snapshot.SchemaVersion)
	}
	if !validIdentifier(snapshot.LineID) || !validIdentifier(snapshot.ProviderID) ||
		!validIdentifier(snapshot.ProcessGeneration) || snapshot.Sequence == 0 || snapshot.ObservedAt.IsZero() {
		return errors.New("snapshot identity, generation, sequence, or observation time is invalid")
	}
	if !validRuntimeCondition(snapshot.Runtime.Condition) || !validCode(snapshot.Runtime.Code) {
		return errors.New("snapshot runtime status is invalid")
	}
	if !validCode(snapshot.Maintenance.Code) || (snapshot.Maintenance.Draining && snapshot.Maintenance.Code != "apply_drain") ||
		(!snapshot.Maintenance.Draining && snapshot.Maintenance.Code != "") {
		return errors.New("snapshot maintenance status is invalid")
	}
	for name, layer := range map[string]LayerStatus{
		"tunnel": snapshot.Tunnel, "ims": snapshot.IMS, "voice": snapshot.Voice, "messaging": snapshot.Messaging,
	} {
		if !validLayerCondition(layer.Condition) || !validCode(layer.Code) || (layer.Condition == LayerReady && !layer.Available) {
			return fmt.Errorf("snapshot %s status is invalid", name)
		}
	}
	if snapshot.ActiveCall != nil && (!validIdentifier(snapshot.ActiveCall.CallID) || !validCallCondition(snapshot.ActiveCall.Condition)) {
		return errors.New("snapshot active call is invalid")
	}
	return nil
}

func (request LifecycleRequest) Validate() error {
	return validateOperationID(request.OperationID)
}

func (request StartCallRequest) Validate() error {
	if err := validateOperationID(request.OperationID); err != nil {
		return err
	}
	if !validIdentifier(request.CallID) || strings.TrimSpace(request.Callee) == "" || len(request.Callee) > 128 {
		return errors.New("call_id or callee is invalid")
	}
	if request.MediaBufferMS < 100 || request.MediaBufferMS > 2000 {
		return errors.New("media_buffer_ms must be between 100 and 2000")
	}
	return nil
}

func (request EndCallRequest) Validate() error {
	if err := validateOperationID(request.OperationID); err != nil {
		return err
	}
	if !validIdentifier(request.CallID) || !validCode(request.ReasonCode) {
		return errors.New("call_id or reason_code is invalid")
	}
	return nil
}

func (request SendMessageRequest) Validate() error {
	if err := validateOperationID(request.OperationID); err != nil {
		return err
	}
	if !validIdentifier(request.MessageID) || strings.TrimSpace(request.Recipient) == "" ||
		len(request.Recipient) > 128 || request.Body == "" || len(request.Body) > 8192 {
		return errors.New("message_id, recipient, or body is invalid")
	}
	return nil
}

func (request MaintenanceRequest) Validate() error {
	return validateOperationID(request.LeaseID)
}

func validateOperationID(value string) error {
	if !validIdentifier(value) {
		return errors.New("operation_id is invalid")
	}
	return nil
}

func validIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-_.:", char) {
			continue
		}
		return false
	}
	return true
}

func validCode(value string) bool {
	return value == "" || validIdentifier(value)
}

func validRuntimeCondition(condition RuntimeCondition) bool {
	switch condition {
	case RuntimeStopped, RuntimeStarting, RuntimeRunning, RuntimeStopping, RuntimeFailed:
		return true
	default:
		return false
	}
}

func validLayerCondition(condition LayerCondition) bool {
	switch condition {
	case LayerUnknown, LayerConnecting, LayerReady, LayerDegraded, LayerBlocked, LayerStopped:
		return true
	default:
		return false
	}
}

func validCallCondition(condition CallCondition) bool {
	switch condition {
	case CallDialing, CallActive, CallEnding:
		return true
	default:
		return false
	}
}
