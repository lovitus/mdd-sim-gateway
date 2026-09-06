package core

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func (server *Server) lineDiagnostics(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if server.catalog == nil || server.eventStore == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "line_diagnostics_unavailable"})
		return
	}
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	query := request.URL.Query()
	if len(query) > 1 || len(query) == 1 && !query.Has("limit") {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_line_diagnostics_request"})
		return
	}
	if _, err := server.catalog.Get(lineID); errors.Is(err, linecatalog.ErrNotFound) {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "line_not_found"})
		return
	} else if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "catalog_read_failed"})
		return
	}
	limit := 200
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_diagnostic_limit"})
			return
		}
		limit = parsed
	}
	entries, scanTruncated, err := server.eventStore.DiagnosticEntries(lineID, limit)
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "line_diagnostics_read_failed"})
		return
	}
	if strings.HasSuffix(request.URL.Path, "/export") {
		response.Header().Set("Content-Disposition", `attachment; filename="mdd-line-diagnostics-redacted.json"`)
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"schema_version": 1, "line_id": lineID, "redacted": true, "scan_truncated": scanTruncated, "entries": entries,
	})
}
