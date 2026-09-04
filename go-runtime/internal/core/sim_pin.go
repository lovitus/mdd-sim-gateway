package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const maximumSIMPINRequestBytes = 4096

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
	if len(stores) > 1 {
		return nil, errors.New("only one SIM PIN operation store is allowed")
	}
	var store *linecatalog.Store
	if len(stores) == 1 {
		store = stores[0]
	}
	return &SIMPINHandler{runtime: runtime, store: store}, nil
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
	requestValue, agentID, process, err := handler.resolve(command)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "sim_pin_target_unavailable"})
		return
	}
	var receipt linecatalog.OperationReceipt
	if handler.store != nil {
		digest := simPINDigest(command)
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
			if existing.State == linecatalog.OperationSucceeded {
				writeJSON(response, http.StatusOK, agentlink.SIMPINResponse{OperationID: command.OperationID, CardID: command.CardID, ReaderName: command.ReaderName, EquipmentID: command.EquipmentID, Action: command.Action, State: "succeeded"})
				return
			}
			writeJSON(response, http.StatusConflict, map[string]string{"code": "sim_pin_operation_requires_reconciliation"})
			return
		}
		now := time.Now().UTC()
		receipt = linecatalog.OperationReceipt{SchemaVersion: linecatalog.OperationSchemaVersion, OperationID: command.OperationID, Kind: linecatalog.OperationProvision, State: linecatalog.OperationPrepared, CreatedAt: now, UpdatedAt: now, RequestDigest: digest, CardID: command.CardID, AgentID: agentID, ProcessGeneration: process, AttachmentID: requestValue.AttachmentID, EquipmentID: requestValue.EquipmentID, SIMSessionGeneration: requestValue.SIMSessionGeneration, Step: "sim_pin", AttemptCount: 1}
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
	}
	result, err := handler.runtime.ExecuteSIMPIN(request.Context(), agentID, process, requestValue)
	if err != nil {
		if handler.store != nil {
			receipt.State = linecatalog.OperationFailed
			receipt.ErrorCode = "sim_pin_operation_failed"
			receipt.ErrorDetail = fmt.Sprintf("%T", err)
			receipt.UpdatedAt = time.Now().UTC()
			_ = handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, receipt.RequestDigest)
		}
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
		writeJSON(response, status, map[string]string{"code": code})
		return
	}
	if handler.store != nil {
		receipt.State = linecatalog.OperationSucceeded
		receipt.OutcomeCode = "sim_pin_verified"
		receipt.UpdatedAt = time.Now().UTC()
		if err := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, receipt.RequestDigest); err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "sim_pin_operation_record_failed"})
			return
		}
	}
	writeJSON(response, http.StatusOK, result)
}

func simPINDigest(command agentlink.SIMPINCommand) string {
	payload := fmt.Sprintf("sim-pin\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%s", command.OperationID, command.CardID, command.ReaderName, command.EquipmentID, command.Action, command.PIN, command.Enabled != nil && *command.Enabled, command.NewPIN)
	hash := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hash[:])
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
