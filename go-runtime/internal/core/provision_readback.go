package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

// ProvisionReadbackHandler performs a fresh read-only modem/SIM observation.
// It owns an independent receipt and never changes catalog desired state.
type ProvisionReadbackHandler struct {
	runtime provisionReconcileRuntime
	store   *linecatalog.Store
}

func NewProvisionReadbackHandler(runtime provisionReconcileRuntime, store *linecatalog.Store) (*ProvisionReadbackHandler, error) {
	if runtime == nil || store == nil {
		return nil, errors.New("provision readback runtime and store are required")
	}
	return &ProvisionReadbackHandler{runtime: runtime, store: store}, nil
}

func (handler *ProvisionReadbackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, maximumProvisionRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumProvisionRequestBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_provision_readback_request"})
		return
	}
	var command agentlink.ProvisionCommand
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF || command.Validate() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_provision_readback_request"})
		return
	}
	if command.APN != "" {
		if line, lookupErr := handler.store.Get(command.LineID); lookupErr == nil {
			for _, profile := range line.Network.APNProfiles {
				if profile.ID == command.APN {
					command.APN = profile.APN
					break
				}
			}
		} else if !errors.Is(lookupErr, linecatalog.ErrNotFound) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_readback_catalog_unavailable"})
			return
		}
	}
	target, err := handler.runtime.ResolveModemTargetForAction(command.EquipmentID, command.CardID, agentlink.ModemCallStatus)
	if err != nil || target.EquipmentID != command.EquipmentID || target.CardID != command.CardID ||
		target.AttachmentID != command.AttachmentID || target.SIMSessionGeneration != command.SIMSessionGeneration {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "provision_readback_target_unavailable"})
		return
	}
	digest := provisionDigest(command)
	if existing, found, lookupErr := handler.store.LookupOperation(command.OperationID, digest); lookupErr != nil {
		status := http.StatusInternalServerError
		if errors.Is(lookupErr, linecatalog.ErrOperationReused) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"code": "provision_readback_operation_unavailable"})
		return
	} else if found {
		if existing.Kind != linecatalog.OperationProvisionReadback {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "provision_readback_operation_unavailable"})
			return
		}
		status := http.StatusAccepted
		if existing.State == linecatalog.OperationSucceeded {
			status = http.StatusOK
		}
		writeJSON(w, status, existing.PublicStatus())
		return
	}
	now := time.Now().UTC()
	receipt := linecatalog.OperationReceipt{
		SchemaVersion: linecatalog.OperationSchemaVersion, OperationID: command.OperationID,
		Kind: linecatalog.OperationProvisionReadback, State: linecatalog.OperationInProgress,
		CreatedAt: now, UpdatedAt: now, RequestDigest: digest,
		PreconditionDigest: provisionIntentDigest(command), LineID: command.LineID,
		CardID: command.CardID, AgentID: target.AgentID, ProcessGeneration: target.ProcessGeneration,
		AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID,
		SIMSessionGeneration: target.SIMSessionGeneration, Step: "provision_readback", AttemptCount: 1,
	}
	if err := handler.store.PutOperation(receipt); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "provision_readback_operation_conflict"})
		return
	}
	result, readErr := handler.runtime.ReconcileProvision(r.Context(), target.AgentID, target.ProcessGeneration,
		agentlink.ProvisionRequest{ProvisionCommand: command, ReadOnly: true})
	receipt.UpdatedAt = time.Now().UTC()
	status := http.StatusAccepted
	if readErr != nil {
		receipt.State = linecatalog.OperationUnknown
		receipt.ErrorCode = "provision_readback_unconfirmed"
	} else if result.Validate() != nil || result.OperationID != command.OperationID ||
		result.EquipmentID != command.EquipmentID || result.CardID != command.CardID ||
		result.SIMSessionGeneration != command.SIMSessionGeneration {
		receipt.State = linecatalog.OperationUnknown
		receipt.ErrorCode = "provision_readback_identity_mismatch"
	} else if result.State == agentlink.ProvisionApplied {
		receipt.State = linecatalog.OperationSucceeded
		receipt.OutcomeCode = "provision_readback_verified"
		status = http.StatusOK
	} else {
		receipt.State = linecatalog.OperationUnknown
		if result.State == agentlink.ProvisionFailed {
			receipt.State = linecatalog.OperationFailed
		}
		receipt.ErrorCode = result.ErrorCode
		receipt.ErrorDetail = result.Error
	}
	if err := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, digest); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "provision_readback_operation_race"})
		return
	}
	writeJSON(w, status, receipt.PublicStatus())
}
