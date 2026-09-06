package core

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/diagnosticlog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type supportAgent struct {
	AgentID      string    `json:"agent_id"`
	LastSeen     time.Time `json:"last_seen"`
	Capabilities []string  `json:"capabilities,omitempty"`
	Platform     string    `json:"platform,omitempty"`
	Architecture string    `json:"architecture,omitempty"`
	BuildVersion string    `json:"build_version,omitempty"`
	Manager      string    `json:"manager,omitempty"`
	ReaderCount  int       `json:"reader_count"`
	ModemCount   int       `json:"modem_count"`
}

type supportDevice struct {
	Kind          string `json:"kind"`
	Mode          string `json:"mode"`
	Condition     string `json:"condition"`
	Code          string `json:"code,omitempty"`
	EndpointCount int    `json:"endpoint_count"`
}

type supportCatalogLine struct {
	LineID                 string `json:"line_id"`
	Enabled                bool   `json:"enabled"`
	HardwareProvisionState string `json:"hardware_provision_state,omitempty"`
	APNProfileCount        int    `json:"apn_profile_count"`
	PCSCFCount             int    `json:"pcscf_count"`
}

type supportLineLog struct {
	LineID  string                   `json:"line_id"`
	Entries []events.DiagnosticEntry `json:"entries"`
}

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
	diagnostic := supportDiagnostics(at, lines, agents)
	deviceSummary := make([]supportDevice, 0, len(devices.Devices))
	for _, device := range devices.Devices {
		deviceSummary = append(deviceSummary, supportDevice{Kind: device.Kind, Mode: device.Mode,
			Condition: device.Condition, Code: device.Code, EndpointCount: len(device.Endpoints)})
	}
	catalogSummary := struct {
		SchemaVersion int                  `json:"schema_version"`
		Revision      uint64               `json:"revision"`
		Lines         []supportCatalogLine `json:"lines"`
	}{SchemaVersion: linecatalog.SchemaVersion, Revision: catalog.Revision, Lines: []supportCatalogLine{}}
	for _, line := range catalog.Lines {
		catalogSummary.Lines = append(catalogSummary.Lines, supportCatalogLine{LineID: line.ID, Enabled: line.Enabled,
			HardwareProvisionState: line.HardwareProvisionState, APNProfileCount: len(line.Network.APNProfiles),
			PCSCFCount: len(line.Network.PCSCF)})
	}
	lineLogs := []supportLineLog{}
	if s.eventStore != nil {
		lineIDs := make([]string, 0, len(catalog.Lines))
		for _, line := range catalog.Lines {
			lineIDs = append(lineIDs, line.ID)
		}
		logsByLine := map[string][]events.DiagnosticEntry{}
		if len(lineIDs) != 0 {
			var logErr error
			logsByLine, _, logErr = s.eventStore.DiagnosticEntriesForLines(lineIDs, 500, 50)
			if logErr != nil {
				writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "support_bundle_unavailable"})
				return
			}
		}
		for _, line := range catalog.Lines {
			entries := logsByLine[line.ID]
			if len(entries) != 0 {
				lineLogs = append(lineLogs, supportLineLog{LineID: line.ID, Entries: entries})
			}
		}
	}
	runtime.Public.TLSFingerprintSHA256 = ""
	runtime.Local = LocalRuntimeInfo{Scope: runtime.Local.Scope, Transport: runtime.Local.Transport}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	files := map[string]any{
		"runtime.json":     runtime,
		"diagnostics.json": diagnostic,
		"devices.json":     deviceSummary,
		"catalog.json":     catalogSummary,
		"line-logs.json":   lineLogs,
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

func supportDiagnostics(at time.Time, lines []events.LineProjection, agents []agentlink.ConnectionStatus) map[string]any {
	agentSummary := make([]supportAgent, 0, len(agents))
	for _, status := range agents {
		agent := supportAgent{AgentID: status.AgentID, LastSeen: status.LastSeen,
			Capabilities: append([]string(nil), status.Capabilities...)}
		if status.Topology != nil {
			agent.ReaderCount, agent.ModemCount = len(status.Topology.Readers), len(status.Topology.Modems)
			if host := status.Topology.Host; host != nil {
				agent.Platform, agent.Architecture, agent.BuildVersion = host.Platform, host.Architecture, host.BuildVersion
				agent.Manager = host.Manager
			}
		}
		agentSummary = append(agentSummary, agent)
	}
	lineFacts := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		facts := make([]map[string]any, 0, len(line.Facts))
		for _, fact := range line.Facts {
			facts = append(facts, map[string]any{"layer": fact.Layer, "condition": fact.Condition,
				"available": fact.Available, "fresh": fact.Fresh, "code": fact.Code,
				"detail": diagnosticlog.RedactString(fact.Detail), "observed_at": fact.ObservedAt,
				"received_at": fact.ReceivedAt})
		}
		lineFacts = append(lineFacts, map[string]any{"line_id": line.LineID, "facts": facts})
	}
	sort.Slice(agentSummary, func(i, j int) bool { return agentSummary[i].AgentID < agentSummary[j].AgentID })
	return map[string]any{"schema_version": 1, "generated_at": at, "agents": agentSummary, "lines": lineFacts}
}
