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

const maximumReaderReadbackRequestBytes = 4096

type ReaderReadbackRuntime interface {
	ResolveCardRoute(string) (agentlink.CardRouteTarget, error)
	ReadReader(context.Context, string, string, agentlink.ReaderReadbackRequest) (agentlink.ReaderReadbackResponse, error)
}

type ReaderReadbackHandler struct {
	runtime ReaderReadbackRuntime
	store   *linecatalog.Store
}

func NewReaderReadbackHandler(runtime ReaderReadbackRuntime, stores ...*linecatalog.Store) (*ReaderReadbackHandler, error) {
	if runtime == nil {
		return nil, errors.New("reader readback runtime is required")
	}
	if len(stores) > 1 {
		return nil, errors.New("only one reader readback operation store is allowed")
	}
	var store *linecatalog.Store
	if len(stores) == 1 {
		store = stores[0]
	}
	return &ReaderReadbackHandler{runtime: runtime, store: store}, nil
}

func (handler *ReaderReadbackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, maximumReaderReadbackRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumReaderReadbackRequestBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_reader_readback_request"})
		return
	}
	var request agentlink.ReaderReadbackRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.Validate() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_reader_readback_request"})
		return
	}
	target, err := handler.runtime.ResolveCardRoute(request.CardID)
	if err != nil || target.Kind != "reader" || target.ReaderName != request.ReaderName ||
		target.ProcessGeneration != request.ProcessGeneration || target.SessionGeneration != request.SIMSessionGeneration {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reader_readback_target_unavailable"})
		return
	}
	digestBytes := sha256.Sum256(payload)
	digest := hex.EncodeToString(digestBytes[:])
	var receipt linecatalog.OperationReceipt
	if handler.store != nil {
		existing, found, lookupErr := handler.store.LookupOperation(request.OperationID, digest)
		if lookupErr != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "reader_readback_operation_unavailable"})
			return
		}
		if found {
			writeJSON(w, http.StatusOK, existing.PublicStatus())
			return
		}
		now := time.Now().UTC()
		receipt = linecatalog.OperationReceipt{
			SchemaVersion: linecatalog.OperationSchemaVersion, OperationID: request.OperationID,
			Kind: linecatalog.OperationReaderReadback, State: linecatalog.OperationInProgress,
			CreatedAt: now, UpdatedAt: now, RequestDigest: digest, CardID: request.CardID,
			AgentID: target.AgentID, ProcessGeneration: target.ProcessGeneration,
			SIMSessionGeneration: request.SIMSessionGeneration, Step: "reader_readback", AttemptCount: 1,
		}
		if err := handler.store.PutOperation(receipt); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "reader_readback_operation_conflict"})
			return
		}
	}
	result, err := handler.runtime.ReadReader(r.Context(), target.AgentID, target.ProcessGeneration, request)
	status, body := http.StatusOK, any(result)
	switch {
	case errors.Is(err, agentlink.ErrReaderSIMIdentityUnsupported):
		receipt.State, receipt.ErrorCode = linecatalog.OperationFailed, "reader_sim_identity_unsupported"
		receipt.Step = "reader_readback_not_dispatched"
		status, body = http.StatusConflict, map[string]any{"code": receipt.ErrorCode, "not_dispatched": true}
	case err != nil:
		receipt.State, receipt.ErrorCode = linecatalog.OperationUnknown, "reader_readback_unconfirmed"
		status, body = http.StatusAccepted, map[string]string{"code": receipt.ErrorCode}
	case result.ValidateFor(request) != nil:
		receipt.State, receipt.ErrorCode = linecatalog.OperationUnknown, "invalid_reader_readback_result"
		status, body = http.StatusBadGateway, map[string]string{"code": receipt.ErrorCode}
	case result.State != "applied":
		receipt.State, receipt.ErrorCode = linecatalog.OperationUnknown, result.ErrorCode
		status = http.StatusAccepted
		if result.State == "failed" {
			receipt.State = linecatalog.OperationFailed
		}
	default:
		receipt.State = linecatalog.OperationSucceeded
		receipt.OutcomeCode = "reader_readback_verified"
	}
	if handler.store != nil {
		receipt.UpdatedAt = time.Now().UTC()
		if err := handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, receipt.RequestDigest); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "reader_readback_operation_race"})
			return
		}
	}
	writeJSON(w, status, body)
}
