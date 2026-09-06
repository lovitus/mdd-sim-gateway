package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type provisionReconcileRuntime interface {
	ResolveModemTargetForAction(string, string, agentlink.ModemAction) (agentlink.ModemTarget, error)
	ReconcileProvision(context.Context, string, string, agentlink.ProvisionRequest) (agentlink.ProvisionResponse, error)
}

type ProvisionReconcileHandler struct {
	runtime provisionReconcileRuntime
	store   *linecatalog.Store
}

func NewProvisionReconcileHandler(runtime provisionReconcileRuntime, store *linecatalog.Store) (*ProvisionReconcileHandler, error) {
	if runtime == nil || store == nil {
		return nil, errors.New("reconcile runtime and store are required")
	}
	return &ProvisionReconcileHandler{runtime: runtime, store: store}, nil
}

func (handler *ProvisionReconcileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, maximumProvisionRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumProvisionRequestBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_reconcile_request"})
		return
	}
	var input provisionAPIRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.ProvisionCommand.Validate() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_reconcile_request"})
		return
	}
	command := input.ProvisionCommand
	receipt, found, err := handler.store.GetOperation(command.OperationID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "reconcile_operation_unavailable"})
		return
	}
	if !found || (receipt.Kind != linecatalog.OperationProvision && receipt.Kind != linecatalog.OperationReprovision) ||
		receipt.State != linecatalog.OperationUnknown {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reconcile_requires_unknown_operation"})
		return
	}
	requestedAPN := command.APN
	selectedAPNID := ""
	var existingLine linecatalog.Line
	if receipt.ExistingLine {
		existingLine, err = handler.store.Get(command.LineID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "reconcile_operation_unavailable"})
			return
		}
		for _, profile := range existingLine.Network.APNProfiles {
			if profile.ID == command.APN {
				selectedAPNID = profile.ID
				command.APN = profile.APN
				break
			}
		}
	}
	digest := provisionDigest(command)
	if receipt.RequestDigest != digest {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reconcile_operation_unavailable"})
		return
	}
	target, err := handler.runtime.ResolveModemTargetForAction(command.EquipmentID, command.CardID, agentlink.ModemCallStatus)
	if err != nil || target.EquipmentID != command.EquipmentID || target.CardID != command.CardID ||
		target.AttachmentID != command.AttachmentID {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reconcile_target_unavailable"})
		return
	}
	result, err := handler.runtime.ReconcileProvision(r.Context(), target.AgentID, target.ProcessGeneration, agentlink.ProvisionRequest{ProvisionCommand: command, ReadOnly: true})
	if err != nil || result.State != agentlink.ProvisionApplied {
		writeJSON(w, http.StatusAccepted, receipt.PublicStatus())
		return
	}
	candidate := provisionLine(command, existingLine, requestedAPN, selectedAPNID,
		receipt.ExistingLine)
	_, updated, err := handler.store.ReconcileProvisionOperation(candidate, command.OperationID, digest,
		"hardware_readback_verified", time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reconcile_operation_race"})
		return
	}
	writeJSON(w, http.StatusOK, updated.PublicStatus())
}
