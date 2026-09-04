package core

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/buildidentity"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

const diagnosticSchemaVersion = 1

// RuntimeInfo describes the deployed transport boundary without exposing
// credentials, private paths, provider loopback addresses, or mutable state.
// Multiple typed WebSocket connections share one public HTTPS/WSS listener;
// media is deliberately not mixed into the state stream because TCP backpressure
// on media must not delay control and topology updates.
type RuntimeInfo struct {
	SchemaVersion int               `json:"schema_version"`
	Component     string            `json:"component"`
	BuildVersion  string            `json:"build_version"`
	GoVersion     string            `json:"go_version"`
	ModuleVersion string            `json:"module_version,omitempty"`
	VCSRevision   string            `json:"vcs_revision,omitempty"`
	VCSTime       *time.Time        `json:"vcs_time,omitempty"`
	VCSModified   *bool             `json:"vcs_modified,omitempty"`
	StateTTL      int               `json:"state_ttl_seconds"`
	Public        PublicRuntimeInfo `json:"public"`
	Local         LocalRuntimeInfo  `json:"local"`
}

type PublicRuntimeInfo struct {
	ListenerCount        int    `json:"listener_count"`
	Listen               string `json:"listen"`
	Transport            string `json:"transport"`
	Multiplexing         string `json:"multiplexing"`
	WebUIPath            string `json:"webui_path"`
	BrowserStatePath     string `json:"browser_state_path"`
	AgentControlPath     string `json:"agent_control_path"`
	BrowserMediaPath     string `json:"browser_media_path"`
	CellularMediaPath    string `json:"cellular_browser_media_path"`
	AgentMediaPath       string `json:"agent_media_path"`
	TLSFingerprintSHA256 string `json:"tls_fingerprint_sha256"`
}

type LocalRuntimeInfo struct {
	Scope     string `json:"scope"`
	Transport string `json:"transport"`
}

// RuntimeInfoForBuild fills process-owned fields. Deployment-owned fields are
// supplied by cmd/mdd-core after strict configuration and TLS validation.
func RuntimeInfoForBuild() RuntimeInfo {
	identity := buildidentity.Read()
	return RuntimeInfo{
		SchemaVersion: diagnosticSchemaVersion,
		Component:     "mdd-core",
		BuildVersion:  identity.DisplayVersion(),
		GoVersion:     identity.GoVersion,
		ModuleVersion: identity.ModuleVersion,
		VCSRevision:   identity.VCSRevision,
		VCSTime:       identity.VCSTime,
		VCSModified:   identity.VCSModified,
		Public: PublicRuntimeInfo{
			ListenerCount:     1,
			Transport:         "https+wss",
			Multiplexing:      "path",
			WebUIPath:         "/",
			BrowserStatePath:  "/v1/browser/ws",
			AgentControlPath:  "/v1/agent/ws",
			BrowserMediaPath:  "/api/browser-media/{sessionID}/ws",
			CellularMediaPath: "/api/cellular-browser-media/{sessionID}/ws",
			AgentMediaPath:    "/v1/agent/media/ws",
		},
		Local: LocalRuntimeInfo{Scope: "literal_loopback", Transport: "http"},
	}
}

type DiagnosticCheck struct {
	ID         string    `json:"id"`
	Scope      string    `json:"scope"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Code       string    `json:"code"`
	Detail     string    `json:"detail,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

type DiagnosticsSnapshot struct {
	SchemaVersion int                          `json:"schema_version"`
	GeneratedAt   time.Time                    `json:"generated_at"`
	Checks        []DiagnosticCheck            `json:"checks"`
	Lines         []events.LineProjection      `json:"lines"`
	Agents        []agentlink.ConnectionStatus `json:"agents"`
}

// DeviceDiagnosticsSnapshot is a read-only, device-scoped view of the same
// typed facts exposed by /v1/diagnostics. It never probes or mutates hardware;
// an ambiguous device identity is returned as a conflict instead of being
// silently associated with another SIM.
type DeviceDiagnosticsSnapshot struct {
	SchemaVersion int               `json:"schema_version"`
	DeviceID      string            `json:"device_id"`
	LineID        string            `json:"line_id,omitempty"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Checks        []DiagnosticCheck `json:"checks"`
}

func (s *Server) deviceDiagnostics(response http.ResponseWriter, request *http.Request) {
	deviceID := strings.TrimSpace(request.PathValue("deviceID"))
	if deviceID == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "device_id_required"})
		return
	}
	snapshot, err := s.currentDevices()
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "device_projection_unavailable"})
		return
	}
	var device *DeviceProjection
	for index := range snapshot.Devices {
		if snapshot.Devices[index].ID == deviceID {
			device = &snapshot.Devices[index]
			break
		}
	}
	if device == nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "device_not_found"})
		return
	}
	at := s.now().UTC()
	result := DeviceDiagnosticsSnapshot{SchemaVersion: diagnosticSchemaVersion, DeviceID: deviceID, GeneratedAt: at,
		Checks: []DiagnosticCheck{{ID: "device.identity", Scope: "device:" + deviceID, Kind: "observation", Status: "pass", Code: "exact_device_projection", Detail: device.Condition}}}
	lineIDs := make(map[string]struct{})
	for _, endpoint := range device.Endpoints {
		if endpoint.Association == "exact" && endpoint.OperationCandidate && endpoint.Line != nil && endpoint.Line.ID != "" {
			lineIDs[endpoint.Line.ID] = struct{}{}
		}
	}
	if len(lineIDs) > 1 {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "device_identity_ambiguous"})
		return
	}
	for lineID := range lineIDs {
		result.LineID = lineID
		for _, projection := range s.replay.Projections(at) {
			if projection.LineID != lineID {
				continue
			}
			for _, fact := range projection.Facts {
				status := string(fact.Condition)
				if !fact.Fresh && status == string(state.ConditionReady) {
					status = string(state.ConditionUnknown)
				}
				result.Checks = append(result.Checks, DiagnosticCheck{
					ID: "line." + lineID + "." + string(fact.Layer), Scope: "line:" + lineID,
					Kind: "observation", Status: status, Code: fact.Code, Detail: fact.Detail,
					ObservedAt: fact.ObservedAt,
				})
			}
		}
	}
	sort.Slice(result.Checks, func(left, right int) bool { return result.Checks[left].ID < result.Checks[right].ID })
	writeJSON(response, http.StatusOK, result)
}

// deviceSMSRefresh reuses the typed cellular SMS list operation after
// resolving the physical device to one exact configured line. It refreshes
// modem SMS facts and history but never sends a message or restarts hardware.
func (s *Server) deviceSMSRefresh(response http.ResponseWriter, request *http.Request) {
	deviceID := strings.TrimSpace(request.PathValue("deviceID"))
	if deviceID == "" || request.URL.RawQuery != "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_device_sms_refresh_request"})
		return
	}
	if s.cellularSMS == nil {
		writeJSON(response, http.StatusNotImplemented, map[string]string{"code": "cellular_sms_unavailable"})
		return
	}
	snapshot, err := s.currentDevices()
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "device_projection_unavailable"})
		return
	}
	var device *DeviceProjection
	for index := range snapshot.Devices {
		if snapshot.Devices[index].ID == deviceID {
			device = &snapshot.Devices[index]
			break
		}
	}
	if device == nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "device_not_found"})
		return
	}
	lineID := ""
	for _, endpoint := range device.Endpoints {
		if endpoint.Association != "exact" || !endpoint.OperationCandidate || endpoint.Line == nil || endpoint.Line.ID == "" {
			continue
		}
		if lineID != "" && lineID != endpoint.Line.ID {
			writeJSON(response, http.StatusConflict, map[string]string{"code": "device_identity_ambiguous"})
			return
		}
		lineID = endpoint.Line.ID
	}
	if lineID == "" {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "device_not_configured"})
		return
	}
	refresh, err := http.NewRequestWithContext(context.WithoutCancel(request.Context()), http.MethodGet,
		"/v1/lines/"+url.PathEscape(lineID)+"/cellular/messages", nil)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "cellular_sms_refresh_request_failed"})
		return
	}
	s.cellularSMS.ServeHTTP(response, refresh)
}

func (s *Server) runtime(response http.ResponseWriter, _ *http.Request) {
	if s.runtimeInfo == nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "runtime_info_unavailable"})
		return
	}
	writeJSON(response, http.StatusOK, *s.runtimeInfo)
}

func (s *Server) diagnostics(response http.ResponseWriter, _ *http.Request) {
	at := s.now().UTC()
	lines := s.replay.Projections(at)
	agents := []agentlink.ConnectionStatus{}
	if s.agents != nil {
		agents = s.agents.Statuses()
	}
	checks := []DiagnosticCheck{
		{ID: "core.public_entry", Scope: "core", Kind: "configuration", Status: "not_run", Code: "runtime_info_unavailable"},
		{ID: "core.local_ipc", Scope: "core", Kind: "configuration", Status: "not_run", Code: "runtime_info_unavailable"},
	}
	if s.runtimeInfo != nil {
		checks[0] = DiagnosticCheck{ID: "core.public_entry", Scope: "core", Kind: "configuration", Status: "pass", Code: "single_public_https_wss", Detail: "Browser, Agent and media routes share one public HTTPS/WSS listener."}
		checks[1] = DiagnosticCheck{ID: "core.local_ipc", Scope: "core", Kind: "configuration", Status: "pass", Code: "local_ipc_loopback_only", Detail: "Provider IPC is restricted to literal loopback and is not a deployment ingress."}
	}
	if received := s.replay.LastReceivedAt(); received.IsZero() {
		checks = append(checks, DiagnosticCheck{ID: "core.state_events", Scope: "core", Kind: "observation", Status: "not_run", Code: "no_state_events"})
	} else {
		checks = append(checks, DiagnosticCheck{ID: "core.state_events", Scope: "core", Kind: "observation", Status: "pass", Code: "state_event_observed", ObservedAt: received.UTC()})
	}
	for _, agent := range agents {
		checks = append(checks, DiagnosticCheck{
			ID: "agent." + agent.AgentID + ".wss", Scope: "agent:" + agent.AgentID,
			Kind: "observation", Status: "pass", Code: "agent_wss_connected",
			Detail: "process_generation=" + agent.ProcessGeneration, ObservedAt: agent.LastSeen.UTC(),
		})
	}
	if s.catalog != nil {
		snapshot, err := s.catalog.Snapshot()
		if err != nil {
			checks = append(checks, DiagnosticCheck{ID: "catalog.read", Scope: "core", Kind: "observation", Status: "fail", Code: "catalog_unavailable"})
		} else {
			for _, line := range snapshot.Lines {
				status, code := "not_run", "line_disabled"
				if line.Enabled {
					status, code = "fail", "provider_route_unavailable"
					if s.providers != nil {
						if generation, ok := s.providers.CurrentGeneration(line.ID); ok {
							status, code = "pass", "provider_route_current"
							checks = append(checks, DiagnosticCheck{ID: "line." + line.ID + ".provider_route", Scope: "line:" + line.ID, Kind: "observation", Status: status, Code: code, Detail: "generation=" + generation})
							continue
						}
					}
				}
				checks = append(checks, DiagnosticCheck{ID: "line." + line.ID + ".provider_route", Scope: "line:" + line.ID, Kind: "observation", Status: status, Code: code})
			}
		}
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	writeJSON(response, http.StatusOK, DiagnosticsSnapshot{
		SchemaVersion: diagnosticSchemaVersion, GeneratedAt: at, Checks: checks, Lines: lines, Agents: agents,
	})
}
