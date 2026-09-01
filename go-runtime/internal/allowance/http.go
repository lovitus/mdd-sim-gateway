package allowance

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

const maximumRequestBytes = 64 << 10

type Handler struct{ service *Service }

func NewHandler(service *Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("allowance service is required")
	}
	return &Handler{service: service}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.RawQuery != "" {
		writeFailure(response, http.StatusBadRequest, "invalid_allowance_request")
		return
	}
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	path := strings.TrimSuffix(request.URL.Path, "/")
	switch {
	case strings.HasSuffix(path, "/allowance"):
		handler.snapshot(response, request, lineID)
	case strings.HasSuffix(path, "/allowance/query-rule"):
		handler.rule(response, request, lineID)
	case strings.HasSuffix(path, "/allowance/query"):
		handler.query(response, request, lineID)
	case strings.Contains(path, "/allowance/query/"):
		handler.queryItem(response, request, lineID, strings.TrimSpace(request.PathValue("queryID")))
	default:
		writeFailure(response, http.StatusNotFound, "allowance_route_not_found")
	}
}

func (handler *Handler) snapshot(response http.ResponseWriter, request *http.Request, lineID string) {
	switch request.Method {
	case http.MethodGet:
		snapshot, err := handler.service.Snapshot(request.Context(), lineID)
		if err != nil {
			handler.writeError(response, lineID, "snapshot", err)
			return
		}
		response.Header().Set("ETag", revisionETag(snapshot.Revision))
		writeJSON(response, http.StatusOK, map[string]any{"snapshot": snapshot})
	case http.MethodPut:
		expected, err := parseIfMatch(request.Header.Get("If-Match"))
		if err != nil {
			writeFailure(response, http.StatusPreconditionRequired, "allowance_if_match_required")
			return
		}
		var values Values
		if decodeRequest(request, &values) != nil {
			writeFailure(response, http.StatusBadRequest, "invalid_allowance_snapshot")
			return
		}
		if _, err := handler.service.line(lineID); err != nil {
			handler.writeError(response, lineID, "snapshot", err)
			return
		}
		snapshot, _, err := handler.service.store.PutSnapshotExpected(lineID, expected, values, handler.service.now().UTC())
		if err != nil {
			handler.writeError(response, lineID, "snapshot", err)
			return
		}
		response.Header().Set("ETag", revisionETag(snapshot.Revision))
		writeJSON(response, http.StatusOK, map[string]any{"snapshot": snapshot})
	default:
		writeFailure(response, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (handler *Handler) rule(response http.ResponseWriter, request *http.Request, lineID string) {
	switch request.Method {
	case http.MethodGet:
		rule, err := handler.service.Rule(lineID)
		if err != nil {
			handler.writeError(response, lineID, "rule", err)
			return
		}
		response.Header().Set("ETag", revisionETag(rule.Revision))
		writeJSON(response, http.StatusOK, map[string]any{"rule": rule})
	case http.MethodPut:
		expected, err := parseIfMatch(request.Header.Get("If-Match"))
		if err != nil {
			writeFailure(response, http.StatusPreconditionRequired, "allowance_rule_if_match_required")
			return
		}
		var input QueryRule
		if decodeRequest(request, &input) != nil {
			writeFailure(response, http.StatusBadRequest, "invalid_allowance_query_rule")
			return
		}
		if _, err := handler.service.line(lineID); err != nil {
			handler.writeError(response, lineID, "rule", err)
			return
		}
		rule, _, err := handler.service.store.PutRuleExpected(lineID, expected, input, handler.service.now().UTC())
		if err != nil {
			handler.writeError(response, lineID, "rule", err)
			return
		}
		response.Header().Set("ETag", revisionETag(rule.Revision))
		writeJSON(response, http.StatusOK, map[string]any{"rule": rule})
	case http.MethodDelete:
		expected, err := parseIfMatch(request.Header.Get("If-Match"))
		if err != nil {
			writeFailure(response, http.StatusPreconditionRequired, "allowance_rule_if_match_required")
			return
		}
		if _, err := handler.service.line(lineID); err != nil {
			handler.writeError(response, lineID, "rule", err)
			return
		}
		rule, _, err := handler.service.store.DeleteRuleExpected(lineID, expected, handler.service.now().UTC())
		if err != nil {
			handler.writeError(response, lineID, "rule", err)
			return
		}
		response.Header().Set("ETag", revisionETag(rule.Revision))
		writeJSON(response, http.StatusOK, map[string]any{"rule": rule})
	default:
		writeFailure(response, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (handler *Handler) query(response http.ResponseWriter, request *http.Request, lineID string) {
	switch request.Method {
	case http.MethodGet:
		view, err := handler.service.QueryView(request.Context(), lineID)
		if err != nil {
			handler.writeError(response, lineID, "query", err)
			return
		}
		writeJSON(response, http.StatusOK, view)
	case http.MethodPost:
		var input QueryRequest
		if decodeRequest(request, &input) != nil {
			writeFailure(response, http.StatusBadRequest, "invalid_allowance_query")
			return
		}
		view, created, err := handler.service.CreateQuery(request.Context(), lineID, input)
		if err != nil {
			handler.writeError(response, lineID, "query", err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(response, status, view)
	default:
		writeFailure(response, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (handler *Handler) queryItem(response http.ResponseWriter, request *http.Request, lineID, queryID string) {
	if request.Method != http.MethodDelete {
		writeFailure(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	view, err := handler.service.CloseQuery(lineID, queryID)
	if err != nil {
		handler.writeError(response, lineID, "query", err)
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (handler *Handler) writeError(response http.ResponseWriter, lineID, representation string, err error) {
	switch {
	case errors.Is(err, linecatalog.ErrNotFound):
		writeFailure(response, http.StatusNotFound, "allowance_line_not_found")
	case errors.Is(err, ErrRevision):
		var revision uint64
		if representation == "rule" {
			if current, readErr := handler.service.store.Rule(lineID); readErr == nil {
				revision = current.Revision
			}
		} else if current, readErr := handler.service.store.Snapshot(lineID); readErr == nil {
			revision = current.Revision
		}
		if revision != 0 {
			response.Header().Set("ETag", revisionETag(revision))
		}
		writeFailure(response, http.StatusPreconditionFailed, "allowance_revision_changed")
	case errors.Is(err, ErrQueryConflict):
		writeFailure(response, http.StatusConflict, "allowance_query_conflict")
	case errors.Is(err, ErrQueryActive):
		writeFailure(response, http.StatusConflict, "allowance_query_active")
	case errors.Is(err, ErrQueryChanged):
		writeFailure(response, http.StatusConflict, "allowance_query_changed")
	case errors.Is(err, ErrRuleUnavailable):
		writeFailure(response, http.StatusConflict, "allowance_query_rule_unavailable")
	case errors.Is(err, ErrRouteUnavailable):
		writeFailure(response, http.StatusPreconditionFailed, "allowance_route_unavailable")
	case errors.Is(err, providermessages.ErrWindowTooLarge):
		writeFailure(response, http.StatusConflict, "allowance_message_window_too_large")
	default:
		writeFailure(response, http.StatusBadRequest, "invalid_allowance_request")
	}
}

func parseIfMatch(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("invalid If-Match")
	}
	return strconv.ParseUint(value[1:len(value)-1], 10, 64)
}

func revisionETag(revision uint64) string { return `"` + strconv.FormatUint(revision, 10) + `"` }

func decodeRequest(request *http.Request, target any) error {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return errors.New("request must use application/json")
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumRequestBytes {
		return errors.New("invalid allowance request body")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("allowance request has trailing JSON")
	}
	return nil
}

func writeFailure(response http.ResponseWriter, status int, code string) {
	writeJSON(response, status, map[string]string{"code": code})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
