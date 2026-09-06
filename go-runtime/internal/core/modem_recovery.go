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
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type ModemRecoveryRuntime interface {
	ExecuteModemRecoveryCommand(context.Context, agentlink.ModemRecoveryCommand) (agentlink.ModemRecoveryResponse, error)
}

type ModemRecoveryHandler struct {
	runtime ModemRecoveryRuntime
	store   *linecatalog.Store
}

func NewModemRecoveryHandler(runtime ModemRecoveryRuntime, store *linecatalog.Store) (*ModemRecoveryHandler, error) {
	if runtime == nil || store == nil {
		return nil, errors.New("modem recovery runtime and store are required")
	}
	return &ModemRecoveryHandler{runtime: runtime, store: store}, nil
}

func (handler *ModemRecoveryHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	var input struct {
		OperationID    string `json:"operation_id"`
		ExpectedCardID string `json:"expected_card_id"`
		EquipmentID    string `json:"equipment_id"`
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, 4097))
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if request.Method != http.MethodPost || request.URL.RawQuery != "" || lineID == "" || err != nil || len(payload) == 0 ||
		len(payload) > 4096 || decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_modem_recovery_request"})
		return
	}
	line, err := handler.store.Get(lineID)
	command := agentlink.ModemRecoveryCommand{OperationID: input.OperationID, EquipmentID: input.EquipmentID,
		CardID: input.ExpectedCardID, Action: agentlink.ModemSoftRestart}
	if err != nil || !line.Enabled || line.CardID != input.ExpectedCardID || command.Validate() != nil {
		writeJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "modem_recovery_target_unavailable"})
		return
	}
	wire, _ := json.Marshal(command)
	digestBytes := sha256.Sum256(wire)
	digest := hex.EncodeToString(digestBytes[:])
	prior, found, err := handler.store.LookupOperation(command.OperationID, digest)
	if errors.Is(err, linecatalog.ErrOperationReused) {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "operation_id_reused"})
		return
	}
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "modem_recovery_operation_unavailable"})
		return
	}
	if found {
		if prior.Kind == linecatalog.OperationModemRecovery && prior.State == linecatalog.OperationSucceeded {
			writeJSON(response, http.StatusOK, agentlink.ModemRecoveryResponse{OperationID: prior.OperationID,
				EquipmentID: prior.EquipmentID, CardID: prior.CardID, AttachmentID: prior.AttachmentID,
				SIMSessionGeneration: prior.SIMSessionGeneration, Action: agentlink.ModemSoftRestart, State: "accepted"})
			return
		}
		writeJSON(response, http.StatusConflict, map[string]string{"code": "modem_recovery_requires_reconciliation"})
		return
	}
	now := time.Now().UTC()
	receipt := linecatalog.OperationReceipt{SchemaVersion: linecatalog.OperationSchemaVersion,
		OperationID: command.OperationID, Kind: linecatalog.OperationModemRecovery, State: linecatalog.OperationInProgress,
		CreatedAt: now, UpdatedAt: now, RequestDigest: digest, LineID: lineID, CardID: command.CardID,
		EquipmentID: command.EquipmentID, Step: command.Action, AttemptCount: 1}
	if err := handler.store.PutOperation(receipt); err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "modem_recovery_operation_conflict"})
		return
	}
	result, executeErr := handler.runtime.ExecuteModemRecoveryCommand(request.Context(), command)
	receipt.UpdatedAt = time.Now().UTC()
	if executeErr != nil {
		receipt.State, receipt.ErrorCode = linecatalog.OperationUnknown, "modem_recovery_transport_unknown"
		status := http.StatusAccepted
		var remote *agentlink.RemoteError
		switch {
		case errors.Is(executeErr, agentlink.ErrModemOffline), errors.Is(executeErr, agentlink.ErrAgentOffline),
			errors.Is(executeErr, agentlink.ErrGenerationMismatch):
			receipt.State, receipt.ErrorCode, status = linecatalog.OperationFailed, "modem_recovery_target_unavailable", http.StatusPreconditionFailed
		case errors.Is(executeErr, agentlink.ErrModemAmbiguous):
			receipt.State, receipt.ErrorCode, status = linecatalog.OperationFailed, "modem_recovery_target_ambiguous", http.StatusConflict
		case errors.As(executeErr, &remote):
			receipt.State, receipt.ErrorCode = linecatalog.OperationFailed, remote.Code
			status = http.StatusConflict
			if remote.Kind == "not_ready" {
				status = http.StatusPreconditionFailed
			}
		}
		_ = handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, digest)
		if receipt.State == linecatalog.OperationUnknown {
			writeJSON(response, status, map[string]string{"code": receipt.ErrorCode})
		} else if result.OperationID != "" {
			writeJSON(response, status, result)
		} else {
			writeJSON(response, status, map[string]string{"code": receipt.ErrorCode})
		}
		return
	}
	receipt.State, receipt.OutcomeCode = linecatalog.OperationSucceeded, "modem_recovery_accepted"
	receipt.AttachmentID, receipt.SIMSessionGeneration = result.AttachmentID, result.SIMSessionGeneration
	if err := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, digest); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "modem_recovery_record_failed"})
		return
	}
	writeJSON(response, http.StatusOK, result)
}
