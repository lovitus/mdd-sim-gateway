package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const maximumSIMPINRequestBytes = 4096
const simPINPreconditionTTL = 2 * time.Minute

// SIMPINRuntime is the Core-side resolver and forwarding boundary. The
// implementation must resolve a live Agent attachment immediately before
// sending the credential-bearing request.
type SIMPINRuntime interface {
	ResolveCardRoute(string) (agentlink.CardRouteTarget, error)
	ResolveModemTargetForAction(string, string, agentlink.ModemAction) (agentlink.ModemTarget, error)
	ExecuteSIMPIN(context.Context, string, string, agentlink.SIMPINRequest) (agentlink.SIMPINResponse, error)
}

type SIMPINHandler struct {
	runtime SIMPINRuntime
	store   *linecatalog.Store
}

func NewSIMPINHandler(runtime SIMPINRuntime, stores ...*linecatalog.Store) (*SIMPINHandler, error) {
	if runtime == nil {
		return nil, errors.New("SIM PIN runtime is required")
	}
	if len(stores) != 1 || stores[0] == nil {
		return nil, errors.New("one SIM PIN operation store is required")
	}
	return &SIMPINHandler{runtime: runtime, store: stores[0]}, nil
}

func (handler *SIMPINHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumSIMPINRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumSIMPINRequestBytes {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_sim_pin_request"})
		return
	}
	var command agentlink.SIMPINCommand
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF || command.Validate() != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_sim_pin_request"})
		return
	}
	digest, err := simPINDigest(handler.store, command)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "sim_pin_operation_unavailable"})
		return
	}
	expectedKind := linecatalog.OperationSIMPIN
	if command.Action == agentlink.SIMPINStatus {
		expectedKind = linecatalog.OperationSIMPINStatus
	}
	existing, found, lookupErr := handler.store.LookupOperation(command.OperationID, digest)
	if errors.Is(lookupErr, linecatalog.ErrOperationReused) {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "operation_id_reused"})
		return
	}
	if lookupErr != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "sim_pin_operation_unavailable"})
		return
	}
	if found {
		if existing.Kind != expectedKind {
			writeJSON(response, http.StatusConflict, map[string]string{"code": "operation_id_reused"})
			return
		}
		if existing.State == linecatalog.OperationSucceeded {
			writeJSON(response, http.StatusOK, replaySIMPIN(command, existing))
			return
		}
		writeJSON(response, http.StatusConflict, map[string]string{"code": "sim_pin_operation_requires_reconciliation"})
		return
	}
	requestValue, agentID, process, err := handler.resolve(command)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "sim_pin_target_unavailable"})
		return
	}
	if command.Action != agentlink.SIMPINStatus {
		if status, code := handler.consumeSIMPINPrecondition(command.PreflightOperationID, requestValue, agentID, process); code != "" {
			writeJSON(response, status, map[string]string{"code": code})
			return
		}
	}
	now := time.Now().UTC()
	receipt := linecatalog.OperationReceipt{SchemaVersion: linecatalog.OperationSchemaVersion,
		OperationID: command.OperationID, Kind: expectedKind, State: linecatalog.OperationPrepared,
		CreatedAt: now, UpdatedAt: now, RequestDigest: digest, CardID: command.CardID,
		AgentID: agentID, ProcessGeneration: process, AttachmentID: requestValue.AttachmentID,
		ReaderName: requestValue.ReaderName, EquipmentID: requestValue.EquipmentID,
		SIMSessionGeneration: requestValue.SIMSessionGeneration,
		Step:                 string(command.Action), AttemptCount: 1}
	if err := handler.store.PutOperation(receipt); err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "sim_pin_operation_conflict"})
		return
	}
	receipt.State = linecatalog.OperationInProgress
	receipt.UpdatedAt = now.Add(time.Nanosecond)
	if err := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationPrepared, digest); err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "sim_pin_operation_conflict"})
		return
	}
	result, err := handler.runtime.ExecuteSIMPIN(request.Context(), agentID, process, requestValue)
	if err != nil {
		code, status := "sim_pin_operation_failed", http.StatusBadGateway
		var remote *agentlink.RemoteError
		if errors.As(err, &remote) {
			code = remote.Code
			if remote.Kind == "not_ready" {
				status = http.StatusServiceUnavailable
			} else if remote.Kind == "conflict" {
				status = http.StatusConflict
			}
		}
		receipt.State = linecatalog.OperationUnknown
		if result.Validate() == nil && (result.State == "failed" || result.State == "unavailable") {
			receipt.State = linecatalog.OperationFailed
		}
		receipt.ErrorCode = code
		receipt.UpdatedAt = time.Now().UTC()
		if recordErr := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, receipt.RequestDigest); recordErr != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "sim_pin_operation_record_failed"})
			return
		}
		if receipt.State == linecatalog.OperationUnknown {
			status = http.StatusAccepted
		}
		if result.Validate() == nil {
			writeJSON(response, status, result)
		} else {
			writeJSON(response, status, map[string]string{"code": code})
		}
		return
	}
	if result.Validate() != nil {
		receipt.State, receipt.ErrorCode = linecatalog.OperationUnknown, "sim_pin_response_invalid"
		receipt.UpdatedAt = time.Now().UTC()
		_ = handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, receipt.RequestDigest)
		writeJSON(response, http.StatusAccepted, map[string]string{"code": "sim_pin_response_invalid"})
		return
	}
	receipt.State = linecatalog.OperationSucceeded
	receipt.OutcomeCode = "sim_pin_verified"
	if command.Action == agentlink.SIMPINStatus {
		receipt.OutcomeCode = "sim_pin_status_observed"
		receipt.PINState = result.State
		receipt.PINAttemptsRemaining = result.AttemptsRemaining
	}
	receipt.UpdatedAt = time.Now().UTC()
	if err := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, receipt.RequestDigest); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "sim_pin_operation_record_failed"})
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func simPINDigest(store *linecatalog.Store, command agentlink.SIMPINCommand) (string, error) {
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	if command.Action != agentlink.SIMPINStatus {
		return store.SecretOperationDigest("sim-pin-v1", payload)
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

func replaySIMPIN(command agentlink.SIMPINCommand, receipt linecatalog.OperationReceipt) agentlink.SIMPINResponse {
	state := "verified"
	var attempts *uint32
	if command.Action == agentlink.SIMPINStatus {
		state = receipt.PINState
		attempts = receipt.PINAttemptsRemaining
	}
	return agentlink.SIMPINResponse{OperationID: command.OperationID, CardID: command.CardID,
		ReaderName: receipt.ReaderName, AttachmentID: receipt.AttachmentID, EquipmentID: receipt.EquipmentID,
		SIMSessionGeneration: receipt.SIMSessionGeneration, Action: command.Action, State: state,
		AttemptsRemaining: attempts}
}

func (handler *SIMPINHandler) consumeSIMPINPrecondition(operationID string, request agentlink.SIMPINRequest,
	agentID, process string) (int, string) {
	if operationID == "" {
		return http.StatusPreconditionRequired, "sim_pin_status_precondition_required"
	}
	receipt, found, err := handler.store.GetOperation(operationID)
	if err != nil {
		return http.StatusInternalServerError, "sim_pin_status_precondition_unavailable"
	}
	now := time.Now().UTC()
	if !found || receipt.Kind != linecatalog.OperationSIMPINStatus || receipt.State != linecatalog.OperationSucceeded ||
		receipt.OutcomeCode != "sim_pin_status_observed" || receipt.PINState != "pin_required" ||
		receipt.PINAttemptsRemaining == nil || *receipt.PINAttemptsRemaining <= 2 ||
		receipt.CardID != request.CardID || receipt.AgentID != agentID || receipt.ProcessGeneration != process ||
		receipt.ReaderName != request.ReaderName || receipt.AttachmentID != request.AttachmentID ||
		receipt.EquipmentID != request.EquipmentID || receipt.SIMSessionGeneration != request.SIMSessionGeneration ||
		now.Before(receipt.UpdatedAt) || now.Sub(receipt.UpdatedAt) > simPINPreconditionTTL {
		return http.StatusPreconditionFailed, "sim_pin_status_precondition_failed"
	}
	receipt.State = linecatalog.OperationReconciled
	receipt.Step = "sim_pin_status_precondition"
	receipt.OutcomeCode = "sim_pin_status_precondition_consumed"
	receipt.UpdatedAt = now
	if err := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationSucceeded, receipt.RequestDigest); err != nil {
		return http.StatusPreconditionFailed, "sim_pin_status_precondition_failed"
	}
	return 0, ""
}

func (handler *SIMPINHandler) resolve(command agentlink.SIMPINCommand) (agentlink.SIMPINRequest, string, string, error) {
	if command.ReaderName != "" {
		target, err := handler.runtime.ResolveCardRoute(command.CardID)
		if err != nil || target.Kind != string(agentlink.AKADeviceReader) || target.CardID != command.CardID || target.ReaderName != command.ReaderName {
			return agentlink.SIMPINRequest{}, "", "", errTarget
		}
		return agentlink.SIMPINRequest{OperationID: command.OperationID, ProcessGeneration: target.ProcessGeneration, CardID: command.CardID, ReaderName: command.ReaderName, SIMSessionGeneration: target.SessionGeneration, Action: command.Action, PIN: command.PIN, NewPIN: command.NewPIN, Enabled: command.Enabled}, target.AgentID, target.ProcessGeneration, nil
	}
	target, err := handler.runtime.ResolveModemTargetForAction(command.EquipmentID, command.CardID, agentlink.ModemCallStatus)
	if err != nil {
		return agentlink.SIMPINRequest{}, "", "", err
	}
	return agentlink.SIMPINRequest{OperationID: command.OperationID, ProcessGeneration: target.ProcessGeneration, CardID: command.CardID, AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID, SIMSessionGeneration: target.SIMSessionGeneration, Action: command.Action, PIN: command.PIN, NewPIN: command.NewPIN, Enabled: command.Enabled}, target.AgentID, target.ProcessGeneration, nil
}

var errTarget = errors.New("SIM PIN target kind or identity mismatch")
