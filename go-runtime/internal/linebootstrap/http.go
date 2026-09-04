package linebootstrap

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const maximumClaimBytes = 4 << 10

type Handler struct{ service *Service }

type claimRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"name"`
	OperationID   string `json:"operation_id,omitempty"`
}

func NewHandler(service *Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("line bootstrap service is required")
	}
	return &Handler{service: service}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if handler == nil || handler.service == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "line_bootstrap_unavailable"})
		return
	}
	if request.Method == http.MethodGet {
		handler.list(response)
		return
	}
	if request.Method == http.MethodPost {
		handler.claim(response, request)
		return
	}
	writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
}

func (handler *Handler) list(response http.ResponseWriter) {
	snapshot, err := handler.service.Project()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "line_candidates_unavailable"})
		return
	}
	response.Header().Set("ETag", revisionETag(snapshot.CatalogRevision))
	writeJSON(response, http.StatusOK, snapshot)
}

func (handler *Handler) claim(response http.ResponseWriter, request *http.Request) {
	candidateID := strings.TrimSpace(request.PathValue("candidateID"))
	decoded, decodeErr := hex.DecodeString(candidateID)
	if decodeErr != nil || len(decoded) != 32 || candidateID != strings.ToLower(candidateID) {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_line_candidate"})
		return
	}
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "catalog_revision_required"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumClaimBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumClaimBytes {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_line_claim"})
		return
	}
	var input claimRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.SchemaVersion != SchemaVersion {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_line_claim"})
		return
	}
	var result ClaimResult
	var claimErr error
	if strings.TrimSpace(input.OperationID) == "" {
		// Legacy clients remain usable; the server allocates an idempotency key.
		result, claimErr = handler.service.Claim(candidateID, input.Name, expected)
	} else {
		result, claimErr = handler.service.ClaimWithOperation(input.OperationID, candidateID, input.Name, expected)
	}
	if errors.Is(claimErr, linecatalog.ErrRevision) {
		response.Header().Set("ETag", revisionETag(result.Revision))
		writeJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "catalog_revision_changed"})
		return
	}
	if errors.Is(claimErr, ErrCandidateStale) {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "line_candidate_stale"})
		return
	}
	if errors.Is(claimErr, ErrCandidateBlocked) {
		code := "line_candidate_blocked"
		if result.Candidate.Condition == "ambiguous_card" {
			code = "card_identity_ambiguous"
		} else if result.Candidate.Condition == "configured" {
			code = "card_identity_in_use"
		}
		writeJSON(response, http.StatusConflict, map[string]string{"code": code})
		return
	}
	if errors.Is(claimErr, linecatalog.ErrCardInUse) {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "card_identity_in_use"})
		return
	}
	if errors.Is(claimErr, linecatalog.ErrOperationReused) {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "operation_id_reused"})
		return
	}
	if claimErr != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_line_claim"})
		return
	}
	response.Header().Set("ETag", revisionETag(result.Revision))
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(response, status, result)
}

func revisionETag(revision uint64) string { return `"` + strconv.FormatUint(revision, 10) + `"` }

func parseIfMatch(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value[1:len(value)-1], `"`) {
		return 0, errors.New("invalid If-Match")
	}
	return strconv.ParseUint(value[1:len(value)-1], 10, 64)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
