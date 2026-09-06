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

const (
	maximumProvisionRequestBytes = 8192
	provisionPreconditionTTL     = 2 * time.Minute
)

type provisionAPIRequest struct {
	agentlink.ProvisionCommand
	PreflightOperationID string `json:"preflight_operation_id,omitempty"`
}

type ProvisionHandler struct {
	runtime     agentlinkProvisionRuntime
	store       *linecatalog.Store
	reprovision bool
	now         func() time.Time
}

type agentlinkProvisionRuntime interface {
	ResolveModemTargetForAction(string, string, agentlink.ModemAction) (agentlink.ModemTarget, error)
	ExecuteProvision(context.Context, string, string, agentlink.ProvisionRequest) (agentlink.ProvisionResponse, error)
}

func NewProvisionHandler(runtime agentlinkProvisionRuntime, store *linecatalog.Store) (*ProvisionHandler, error) {
	if runtime == nil {
		return nil, errors.New("provision runtime is required")
	}
	return &ProvisionHandler{runtime: runtime, store: store, now: time.Now}, nil
}

func NewReprovisionHandler(runtime agentlinkProvisionRuntime, store *linecatalog.Store) (*ProvisionHandler, error) {
	handler, err := NewProvisionHandler(runtime, store)
	if err != nil {
		return nil, err
	}
	handler.reprovision = true
	return handler, nil
}

func (handler *ProvisionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, maximumProvisionRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumProvisionRequestBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_provision_request"})
		return
	}
	var input provisionAPIRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.ProvisionCommand.Validate() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_provision_request"})
		return
	}
	command := input.ProvisionCommand
	requestedAPN := command.APN
	selectedAPNID := ""
	var existingLine linecatalog.Line
	existingLineFound := false
	if handler.store != nil {
		existing, lookupErr := handler.store.Get(command.LineID)
		if lookupErr == nil {
			existingLine, existingLineFound = existing, true
		} else if !errors.Is(lookupErr, linecatalog.ErrNotFound) || handler.reprovision {
			status := http.StatusInternalServerError
			if errors.Is(lookupErr, linecatalog.ErrNotFound) {
				status = http.StatusConflict
			}
			writeJSON(w, status, map[string]string{"code": "provision_catalog_unavailable"})
			return
		}
	}
	if !handler.reprovision && existingLineFound &&
		(existingLine.HardwareProvisionState != "draft" || existingLine.Enabled || command.Enabled) {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "provision_requires_disabled_draft"})
		return
	}
	if handler.reprovision && existingLine.HardwareProvisionState == "draft" {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "draft_requires_first_provision"})
		return
	}
	if existingLineFound {
		if command.APN != "" {
			for _, profile := range existingLine.Network.APNProfiles {
				if profile.ID == command.APN {
					selectedAPNID = profile.ID
					command.APN = profile.APN
					break
				}
			}
		}
	}
	if handler.reprovision && handler.store == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_catalog_unavailable"})
		return
	}
	digest := provisionDigest(command)
	if handler.store != nil {
		existing, found, lookupErr := handler.store.LookupOperation(command.OperationID, digest)
		if errors.Is(lookupErr, linecatalog.ErrOperationReused) {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "operation_id_reused"})
			return
		}
		if lookupErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_operation_unavailable"})
			return
		}
		if found {
			expectedKind := linecatalog.OperationProvision
			if handler.reprovision {
				expectedKind = linecatalog.OperationReprovision
			}
			if existing.Kind != expectedKind {
				writeJSON(w, http.StatusConflict, map[string]string{"code": "operation_id_reused"})
				return
			}
			status := http.StatusConflict
			if existing.State == linecatalog.OperationSucceeded {
				status = http.StatusOK
			} else if existing.State == linecatalog.OperationUnknown {
				status = http.StatusAccepted
			}
			writeJSON(w, status, existing.PublicStatus())
			return
		}
	}
	previouslyEnabled := handler.reprovision && existingLine.Enabled
	target, err := handler.runtime.ResolveModemTargetForAction(command.EquipmentID, command.CardID, agentlink.ModemCallStatus)
	if err != nil || target.EquipmentID != command.EquipmentID || target.CardID != command.CardID ||
		target.AttachmentID != command.AttachmentID {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "provision_target_unavailable"})
		return
	}
	if status, code := handler.consumeProvisionPrecondition(input.PreflightOperationID, command, target); code != "" {
		writeJSON(w, status, map[string]string{"code": code})
		return
	}
	candidateLine := provisionLine(command, existingLine, requestedAPN, selectedAPNID, existingLineFound)
	var receipt linecatalog.OperationReceipt
	hasReceipt := false
	if handler.store != nil {
		now := handler.now().UTC()
		receipt = linecatalog.OperationReceipt{
			SchemaVersion: linecatalog.OperationSchemaVersion, OperationID: command.OperationID,
			Kind: linecatalog.OperationProvision, State: linecatalog.OperationPrepared,
			CreatedAt: now, UpdatedAt: now, RequestDigest: digest, LineID: command.LineID,
			CardID: command.CardID, AgentID: target.AgentID, ProcessGeneration: target.ProcessGeneration,
			AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID,
			SIMSessionGeneration: command.SIMSessionGeneration, Step: "provision", AttemptCount: 1,
		}
		enableAfterSuccess := command.Enabled
		if handler.reprovision {
			enableAfterSuccess = previouslyEnabled
		}
		receipt.EnableAfterSuccess = &enableAfterSuccess
		receipt.ExistingLine = existingLineFound
		snapshot, snapshotErr := handler.store.Snapshot()
		if snapshotErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_catalog_unavailable"})
			return
		}
		receipt.ExpectedCatalogRevision = snapshot.Revision
		var committed linecatalog.OperationReceipt
		if handler.reprovision {
			receipt.Kind = linecatalog.OperationReprovision
		}
		if existingLineFound {
			_, committed, err = handler.store.BeginExistingProvisionOperation(command.LineID, snapshot.Revision, receipt)
		} else {
			_, committed, err = handler.store.CreateExpectedWithOperation(candidateLine, snapshot.Revision, receipt)
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "provision_catalog_conflict"})
			return
		}
		receipt = committed
		hasReceipt = true
	}
	result, err := handler.runtime.ExecuteProvision(r.Context(), target.AgentID, target.ProcessGeneration, agentlink.ProvisionRequest{ProvisionCommand: command})
	if err != nil {
		result = agentlink.ProvisionResponse{OperationID: command.OperationID, State: agentlink.ProvisionUnknown,
			EquipmentID: command.EquipmentID, CardID: command.CardID, SIMSessionGeneration: command.SIMSessionGeneration,
			Step: "transport", ErrorCode: "provision_unconfirmed"}
		if hasReceipt {
			receipt.State = linecatalog.OperationUnknown
			receipt.ErrorCode = result.ErrorCode
			receipt.UpdatedAt = handler.now().UTC()
			if recordErr := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationCatalogCommitted, digest); recordErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_operation_record_failed"})
				return
			}
		}
		writeJSON(w, http.StatusAccepted, result)
		return
	}
	if result.Validate() != nil || result.OperationID != command.OperationID ||
		result.EquipmentID != command.EquipmentID || result.CardID != command.CardID ||
		result.SIMSessionGeneration != command.SIMSessionGeneration {
		result.State = agentlink.ProvisionUnknown
		result.ErrorCode = "provision_response_identity_mismatch"
		result.Error = "Agent provision response did not match the requested identity"
	}
	if result.State == agentlink.ProvisionUnknown {
		if hasReceipt {
			receipt.State = linecatalog.OperationUnknown
			receipt.ErrorCode = result.ErrorCode
			receipt.ErrorDetail = result.Error
			receipt.UpdatedAt = time.Now().UTC()
			if recordErr := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationCatalogCommitted, digest); recordErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_operation_record_failed"})
				return
			}
		}
		writeJSON(w, http.StatusAccepted, result)
		return
	}
	if result.State == agentlink.ProvisionFailed {
		if hasReceipt {
			if _, _, recordErr := handler.store.FailProvisionOperation(command.OperationID, digest,
				result.ErrorCode, result.Error, handler.now().UTC()); recordErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_operation_record_failed"})
				return
			}
		}
		writeJSON(w, http.StatusBadGateway, result)
		return
	}
	if result.State != agentlink.ProvisionApplied {
		if hasReceipt {
			receipt.State = linecatalog.OperationUnknown
			receipt.ErrorCode = "provision_unrecognized_state"
			receipt.ErrorDetail = "Agent returned a non-terminal provision state"
			receipt.UpdatedAt = time.Now().UTC()
			if recordErr := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationCatalogCommitted, digest); recordErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_operation_record_failed"})
				return
			}
		}
		result.State = agentlink.ProvisionUnknown
		result.ErrorCode = "provision_unrecognized_state"
		writeJSON(w, http.StatusAccepted, result)
		return
	}
	if hasReceipt {
		if existingLineFound {
			if _, _, finalizeErr := handler.store.FinalizeExistingProvision(candidateLine, command.OperationID, digest, handler.now().UTC()); finalizeErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_operation_record_failed"})
				return
			}
		} else if _, _, finalizeErr := handler.store.FinalizeProvision(command.LineID, command.OperationID, digest,
			command.Enabled, handler.now().UTC()); finalizeErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_operation_record_failed"})
			return
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func provisionLine(command agentlink.ProvisionCommand, existing linecatalog.Line, requestedAPN, selectedAPNID string,
	reprovision bool) linecatalog.Line {
	line := linecatalog.Line{
		SchemaVersion: linecatalog.SchemaVersion, ID: command.LineID, Name: command.LineName,
		Enabled: false, CardID: command.CardID, HardwareProvisionState: "draft",
		SIM: linecatalog.SIMConfig{IMSI: command.IMSI, MCC: command.MCC, MNC: command.MNC,
			IMEI: command.IMEI, IMEISV: command.IMEISV, MSISDN: command.MSISDN, SMSC: command.SMSC},
		Network: provisionNetwork(command),
	}
	if !reprovision {
		return line
	}
	existing.Name = command.LineName
	existing.Enabled = false
	existing.CardID = command.CardID
	existing.SIM = line.SIM
	existing.Network.EgressCountry = command.EgressCountry
	if command.IMSAPN != "" {
		existing.Network.IMSAPN = command.IMSAPN
	}
	if command.IDRMode != "" {
		existing.Network.IDRMode = command.IDRMode
	}
	if command.CPMode != "" {
		existing.Network.CPMode = command.CPMode
	}
	switch {
	case requestedAPN == "":
		existing.Network.ActiveAPN = ""
	case selectedAPNID != "":
		existing.Network.ActiveAPN = selectedAPNID
	default:
		updated := false
		for index := range existing.Network.APNProfiles {
			if existing.Network.APNProfiles[index].ID == "provision-apn" {
				existing.Network.APNProfiles[index].Name = "Provisioned APN"
				existing.Network.APNProfiles[index].APN = command.APN
				existing.Network.APNProfiles[index].Auth = "NONE"
				existing.Network.APNProfiles[index].Username = ""
				existing.Network.APNProfiles[index].Password = ""
				existing.Network.APNProfiles[index].PasswordSet = false
				updated = true
				break
			}
		}
		if !updated {
			existing.Network.APNProfiles = append(existing.Network.APNProfiles, linecatalog.APNProfile{
				ID: "provision-apn", Name: "Provisioned APN", APN: command.APN, Auth: "NONE",
			})
		}
		existing.Network.ActiveAPN = "provision-apn"
	}
	return existing
}

func (handler *ProvisionHandler) consumeProvisionPrecondition(operationID string, command agentlink.ProvisionCommand,
	target agentlink.ModemTarget) (int, string) {
	if handler.store == nil {
		return http.StatusInternalServerError, "provision_operation_unavailable"
	}
	if operationID == "" {
		return http.StatusPreconditionRequired, "provision_precondition_required"
	}
	receipt, found, err := handler.store.GetOperation(operationID)
	if err != nil {
		return http.StatusInternalServerError, "provision_precondition_unavailable"
	}
	now := handler.now().UTC()
	if !found || receipt.Kind != linecatalog.OperationProvisionReadback ||
		receipt.State != linecatalog.OperationSucceeded || receipt.OutcomeCode != "provision_readback_verified" ||
		receipt.PreconditionDigest != provisionIntentDigest(command) || receipt.LineID != command.LineID ||
		receipt.CardID != target.CardID || receipt.AgentID != target.AgentID ||
		receipt.ProcessGeneration != target.ProcessGeneration || receipt.AttachmentID != target.AttachmentID ||
		receipt.EquipmentID != target.EquipmentID || receipt.SIMSessionGeneration != command.SIMSessionGeneration ||
		now.Before(receipt.UpdatedAt) || now.Sub(receipt.UpdatedAt) > provisionPreconditionTTL {
		return http.StatusPreconditionFailed, "provision_precondition_failed"
	}
	receipt.State = linecatalog.OperationReconciled
	receipt.UpdatedAt = now
	receipt.Step = "provision_precondition"
	receipt.OutcomeCode = "provision_precondition_consumed"
	if err := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationSucceeded, receipt.RequestDigest); err != nil {
		return http.StatusPreconditionFailed, "provision_precondition_failed"
	}
	return 0, ""
}

func provisionNetwork(command agentlink.ProvisionCommand) linecatalog.NetworkConfig {
	network := linecatalog.NetworkConfig{EgressCountry: command.EgressCountry,
		IMSAPN: command.IMSAPN, IDRMode: command.IDRMode, CPMode: command.CPMode}
	if command.APN != "" {
		network.APNProfiles = []linecatalog.APNProfile{{
			ID: "provision-apn", Name: "Provisioned APN", APN: command.APN, Auth: "NONE",
		}}
		network.ActiveAPN = "provision-apn"
	}
	return network
}

func provisionDigest(command agentlink.ProvisionCommand) string {
	payload, _ := json.Marshal(command)
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func provisionIntentDigest(command agentlink.ProvisionCommand) string {
	command.OperationID = ""
	return provisionDigest(command)
}
