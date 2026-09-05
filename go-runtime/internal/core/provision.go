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

const maximumProvisionRequestBytes = 8192

type ProvisionHandler struct {
	runtime     agentlinkProvisionRuntime
	store       *linecatalog.Store
	reprovision bool
}

type agentlinkProvisionRuntime interface {
	ResolveModemTargetForAction(string, string, agentlink.ModemAction) (agentlink.ModemTarget, error)
	ExecuteProvision(context.Context, string, string, agentlink.ProvisionRequest) (agentlink.ProvisionResponse, error)
}

func NewProvisionHandler(runtime agentlinkProvisionRuntime, store *linecatalog.Store) (*ProvisionHandler, error) {
	if runtime == nil {
		return nil, errors.New("provision runtime is required")
	}
	return &ProvisionHandler{runtime: runtime, store: store}, nil
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
	var command agentlink.ProvisionCommand
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF || command.Validate() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_provision_request"})
		return
	}
	if handler.reprovision && handler.store != nil && command.APN != "" {
		existing, lookupErr := handler.store.Get(command.LineID)
		if lookupErr != nil && !errors.Is(lookupErr, linecatalog.ErrNotFound) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_catalog_unavailable"})
			return
		}
		if lookupErr == nil {
			for _, profile := range existing.Network.APNProfiles {
				if profile.ID == command.APN {
					command.APN = profile.APN
					break
				}
			}
		}
	}
	previouslyEnabled := false
	if handler.reprovision && handler.store != nil {
		if existing, lookupErr := handler.store.Get(command.LineID); lookupErr == nil {
			previouslyEnabled = existing.Enabled
		} else if !errors.Is(lookupErr, linecatalog.ErrNotFound) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_catalog_unavailable"})
			return
		}
	}
	target, err := handler.runtime.ResolveModemTargetForAction(command.EquipmentID, command.CardID, agentlink.ModemCallStatus)
	if err != nil || target.EquipmentID != command.EquipmentID || target.CardID != command.CardID ||
		target.AttachmentID != command.AttachmentID || target.SIMSessionGeneration != command.SIMSessionGeneration {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "provision_target_unavailable"})
		return
	}
	digest := provisionDigest(command)
	var receipt linecatalog.OperationReceipt
	hasReceipt := false
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
			status := http.StatusConflict
			if existing.State == linecatalog.OperationSucceeded {
				status = http.StatusOK
			} else if existing.State == linecatalog.OperationUnknown {
				status = http.StatusAccepted
			}
			writeJSON(w, status, existing.PublicStatus())
			return
		}
		now := time.Now().UTC()
		receipt = linecatalog.OperationReceipt{
			SchemaVersion: linecatalog.OperationSchemaVersion, OperationID: command.OperationID,
			Kind: linecatalog.OperationProvision, State: linecatalog.OperationPrepared,
			CreatedAt: now, UpdatedAt: now, RequestDigest: digest, LineID: command.LineID,
			CardID: command.CardID, AgentID: target.AgentID, ProcessGeneration: target.ProcessGeneration,
			AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID,
			SIMSessionGeneration: target.SIMSessionGeneration, Step: "provision", AttemptCount: 1,
		}
		line := linecatalog.Line{
			SchemaVersion: linecatalog.SchemaVersion, ID: command.LineID, Name: command.LineName,
			Enabled: false, CardID: command.CardID,
			SIM:     linecatalog.SIMConfig{IMSI: command.IMSI, MCC: command.MCC, MNC: command.MNC, IMEI: command.IMEI, MSISDN: command.MSISDN, SMSC: command.SMSC},
			Network: provisionNetwork(command),
		}
		snapshot, snapshotErr := handler.store.Snapshot()
		if snapshotErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_catalog_unavailable"})
			return
		}
		receipt.ExpectedCatalogRevision = snapshot.Revision
		var committed linecatalog.OperationReceipt
		if handler.reprovision {
			receipt.Kind = linecatalog.OperationReprovision
			_, committed, err = handler.store.UpdateExpectedWithOperation(line, snapshot.Revision, receipt)
		} else {
			_, committed, err = handler.store.CreateExpectedWithOperation(line, snapshot.Revision, receipt)
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
		if hasReceipt {
			receipt.State = linecatalog.OperationFailed
			receipt.ErrorCode = "provision_failed"
			receipt.UpdatedAt = time.Now().UTC()
			if recordErr := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationCatalogCommitted, digest); recordErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_operation_record_failed"})
				return
			}
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"code": "provision_failed"})
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
			receipt.State = linecatalog.OperationFailed
			receipt.ErrorCode = result.ErrorCode
			receipt.ErrorDetail = result.Error
			receipt.UpdatedAt = time.Now().UTC()
			if recordErr := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationCatalogCommitted, digest); recordErr != nil {
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
		enabled := command.Enabled
		if handler.reprovision {
			enabled = previouslyEnabled
		}
		if _, _, finalizeErr := handler.store.FinalizeProvision(command.LineID, command.OperationID, digest, enabled, time.Now().UTC()); finalizeErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "provision_operation_record_failed"})
			return
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func provisionNetwork(command agentlink.ProvisionCommand) linecatalog.NetworkConfig {
	network := linecatalog.NetworkConfig{EgressCountry: command.EgressCountry}
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
