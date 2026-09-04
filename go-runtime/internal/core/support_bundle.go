package core

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

// supportBundle serves a bounded, read-only diagnostic archive. It deliberately
// contains projections only: credentials, PINs, tokens, raw USB session data,
// and mutable operation payloads never enter the archive.
func (s *Server) supportBundle(response http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_support_bundle_request"})
		return
	}
	at := s.now().UTC()
	devices, err := s.currentDevices()
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "support_bundle_unavailable"})
		return
	}
	agents := []agentlink.ConnectionStatus{}
	if s.agents != nil {
		agents = s.agents.Statuses()
	}
	lines := []events.LineProjection{}
	if s.replay != nil {
		lines = s.replay.Projections(at)
	}
	catalog := linecatalog.Snapshot{SchemaVersion: linecatalog.SchemaVersion, Lines: []linecatalog.Line{}}
	if s.catalog != nil {
		catalog, err = s.catalog.Snapshot()
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "support_bundle_unavailable"})
			return
		}
	}
	runtime := RuntimeInfo{}
	if s.runtimeInfo != nil {
		runtime = *s.runtimeInfo
	}
	diagnostic := DiagnosticsSnapshot{SchemaVersion: diagnosticSchemaVersion, GeneratedAt: at, Checks: []DiagnosticCheck{}, Lines: lines, Agents: agents}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	files := map[string]any{
		"runtime.json":     runtime,
		"diagnostics.json": diagnostic,
		"devices.json":     devices,
		"catalog.json":     catalog,
	}
	for name, value := range files {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "support_bundle_failed"})
			return
		}
		payload, marshalErr := json.MarshalIndent(value, "", "  ")
		if marshalErr != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "support_bundle_failed"})
			return
		}
		if _, writeErr := entry.Write(append(payload, '\n')); writeErr != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "support_bundle_failed"})
			return
		}
	}
	if err := writer.Close(); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "support_bundle_failed"})
		return
	}
	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set("Content-Disposition", `attachment; filename="mdd-support-redacted.zip"`)
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(archive.Bytes())
}
