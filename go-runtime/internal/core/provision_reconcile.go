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
	digest := provisionDigest(command)
	receipt, found, err := handler.store.LookupOperation(command.OperationID, digest)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, linecatalog.ErrOperationReused) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"code": "reconcile_operation_unavailable"})
		return
	}
	if !found || (receipt.Kind != linecatalog.OperationProvision && receipt.Kind != linecatalog.OperationReprovision) ||
		receipt.State != linecatalog.OperationUnknown {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reconcile_requires_unknown_operation"})
		return
	}
	target, err := handler.runtime.ResolveModemTargetForAction(command.EquipmentID, command.CardID, agentlink.ModemCallStatus)
	if err != nil || target.EquipmentID != command.EquipmentID || target.CardID != command.CardID ||
		target.AttachmentID != command.AttachmentID || target.SIMSessionGeneration != command.SIMSessionGeneration {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reconcile_target_unavailable"})
		return
	}
	result, err := handler.runtime.ReconcileProvision(r.Context(), target.AgentID, target.ProcessGeneration, agentlink.ProvisionRequest{ProvisionCommand: command, ReadOnly: true})
	if err != nil || result.State != agentlink.ProvisionApplied {
		writeJSON(w, http.StatusAccepted, receipt.PublicStatus())
		return
	}
	_, updated, err := handler.store.ReconcileProvisionOperation(command.OperationID, digest, "hardware_readback_verified", time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reconcile_operation_race"})
		return
	}
	writeJSON(w, http.StatusOK, updated.PublicStatus())
}
