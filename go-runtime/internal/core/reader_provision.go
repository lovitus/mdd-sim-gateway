package core

import (
	"bytes"
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

type readerProvisionRequest struct {
	SchemaVersion           int    `json:"schema_version"`
	OperationID             string `json:"operation_id"`
	LineID                  string `json:"line_id"`
	ExpectedCatalogRevision uint64 `json:"expected_catalog_revision"`
	ProcessGeneration       string `json:"process_generation"`
	ReaderName              string `json:"reader_name"`
	CardID                  string `json:"card_id"`
	SIMSessionGeneration    string `json:"sim_session_generation"`
}

type readerProvisionResponse struct {
	SchemaVersion   int               `json:"schema_version"`
	OperationID     string            `json:"operation_id"`
	State           string            `json:"state"`
	CatalogRevision uint64            `json:"catalog_revision,omitempty"`
	Line            *linecatalog.Line `json:"line,omitempty"`
	ErrorCode       string            `json:"error_code,omitempty"`
}

type ReaderProvisionHandler struct {
	runtime ReaderReadbackRuntime
	store   *linecatalog.Store
	now     func() time.Time
}

func NewReaderProvisionHandler(runtime ReaderReadbackRuntime, store *linecatalog.Store) (*ReaderProvisionHandler, error) {
	if runtime == nil || store == nil {
		return nil, errors.New("reader provision runtime and store are required")
	}
	return &ReaderProvisionHandler{runtime: runtime, store: store, now: time.Now}, nil
}

func (handler *ReaderProvisionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, maximumReaderReadbackRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumReaderReadbackRequestBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_reader_provision_request"})
		return
	}
	var input readerProvisionRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_reader_provision_request"})
		return
	}
	readback := input.readback()
	if input.SchemaVersion != 1 || strings.TrimSpace(input.LineID) == "" || input.ExpectedCatalogRevision == 0 || readback.Validate() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_reader_provision_request"})
		return
	}
	canonical, err := json.Marshal(input)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "reader_provision_operation_unavailable"})
		return
	}
	digestBytes := sha256.Sum256(canonical)
	digest := hex.EncodeToString(digestBytes[:])
	if existing, found, lookupErr := handler.store.LookupOperation(input.OperationID, digest); found || lookupErr != nil {
		if errors.Is(lookupErr, linecatalog.ErrOperationReused) {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "operation_id_reused"})
			return
		}
		if lookupErr != nil || existing.Kind != linecatalog.OperationReaderProvision {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "reader_provision_operation_unavailable"})
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
	target, err := handler.runtime.ResolveCardRoute(input.CardID)
	if err != nil || target.Kind != string(agentlink.AKADeviceReader) || target.AgentID == "" ||
		target.ProcessGeneration != input.ProcessGeneration || target.ReaderName != input.ReaderName ||
		target.SessionGeneration != input.SIMSessionGeneration || target.CardID != input.CardID {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reader_provision_target_unavailable"})
		return
	}
	line, revision, err := handler.store.GetWithRevision(input.LineID)
	if err != nil || revision != input.ExpectedCatalogRevision || line.HardwareProvisionState != "draft" ||
		line.Enabled || line.CardID != input.CardID {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reader_provision_requires_disabled_draft"})
		return
	}
	now := handler.now().UTC()
	receipt := linecatalog.OperationReceipt{
		SchemaVersion: linecatalog.OperationSchemaVersion, OperationID: input.OperationID,
		Kind: linecatalog.OperationReaderProvision, State: linecatalog.OperationInProgress,
		CreatedAt: now, UpdatedAt: now, RequestDigest: digest,
		ExpectedCatalogRevision: input.ExpectedCatalogRevision, LineID: input.LineID, CardID: input.CardID,
		AgentID: target.AgentID, ProcessGeneration: target.ProcessGeneration, ReaderName: target.ReaderName,
		SIMSessionGeneration: target.SessionGeneration, Step: "reader_provision_readback", AttemptCount: 1,
		ExistingLine: true,
	}
	result, readErr := handler.runtime.ReadReader(r.Context(), target.AgentID, target.ProcessGeneration, readback)
	if readErr != nil {
		receipt.State = linecatalog.OperationUnknown
		receipt.ErrorCode = "reader_provision_unconfirmed"
		receipt.UpdatedAt = handler.now().UTC()
		if handler.store.PutOperation(receipt) != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "reader_provision_operation_record_failed"})
			return
		}
		writeJSON(w, http.StatusAccepted, readerProvisionResponse{SchemaVersion: 1, OperationID: input.OperationID,
			State: string(linecatalog.OperationUnknown), ErrorCode: "reader_provision_unconfirmed"})
		return
	}
	if result.ValidateFor(readback) != nil || result.State != "applied" || result.Reader == nil ||
		result.Reader.CardID != input.CardID || result.Reader.ReaderName != input.ReaderName ||
		result.Reader.SessionGeneration != input.SIMSessionGeneration {
		state, code, status := linecatalog.OperationUnknown, "reader_provision_response_invalid", http.StatusAccepted
		if result.ValidateFor(readback) == nil && result.State == "failed" {
			state, code, status = linecatalog.OperationFailed, result.ErrorCode, http.StatusBadGateway
		}
		receipt.State, receipt.ErrorCode, receipt.ErrorDetail, receipt.UpdatedAt = state, code, code, handler.now().UTC()
		err = handler.store.PutOperation(receipt)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "reader_provision_operation_record_failed"})
			return
		}
		writeJSON(w, status, readerProvisionResponse{SchemaVersion: 1, OperationID: input.OperationID,
			State: string(state), ErrorCode: code})
		return
	}
	receipt.State = linecatalog.OperationSucceeded
	receipt.Step = "reader_provision_commit"
	receipt.OutcomeCode = "reader_provision_verified"
	receipt.UpdatedAt = handler.now().UTC()
	line, final, err := handler.store.FinalizeReaderProvision(input.LineID, input.ExpectedCatalogRevision, receipt)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "reader_provision_catalog_changed"})
		return
	}
	writeJSON(w, http.StatusOK, readerProvisionResponse{SchemaVersion: 1, OperationID: input.OperationID,
		State: string(final.State), CatalogRevision: final.CommittedCatalogRevision, Line: &line})
}

func (input readerProvisionRequest) readback() agentlink.ReaderReadbackRequest {
	return agentlink.ReaderReadbackRequest{OperationID: input.OperationID, ProcessGeneration: input.ProcessGeneration,
		ReaderName: input.ReaderName, CardID: input.CardID, SIMSessionGeneration: input.SIMSessionGeneration}
}
