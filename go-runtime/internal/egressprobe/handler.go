package egressprobe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
)

const defaultTimeout = 8 * time.Second

type probeFunc func(context.Context, string) (Result, error)

type Handler struct {
	statusPath string
	timeout    time.Duration
	probe      probeFunc
}

type ExitStatus struct {
	Country        string `json:"country"`
	Ready          bool   `json:"ready"`
	Mode           string `json:"mode,omitempty"`
	Node           string `json:"node,omitempty"`
	CandidateCount int    `json:"candidate_count,omitempty"`
	Error          string `json:"error,omitempty"`
	Testable       bool   `json:"testable"`
}

func NewHandler(statusPath string, timeout time.Duration) (*Handler, error) {
	statusPath = strings.TrimSpace(statusPath)
	if statusPath == "" {
		return nil, errors.New("egress status path is required")
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < time.Second || timeout > 30*time.Second {
		return nil, errors.New("egress probe timeout must be between 1 and 30 seconds")
	}
	return &Handler{statusPath: statusPath, timeout: timeout, probe: Probe}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if handler == nil || handler.probe == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "egress_diagnostic_unavailable"})
		return
	}
	if request.Method == http.MethodGet {
		handler.list(response)
		return
	}
	if request.Method == http.MethodPost {
		handler.test(response, request)
		return
	}
	writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
}

func (handler *Handler) list(response http.ResponseWriter) {
	snapshot, err := egressstatus.Load(handler.statusPath)
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{
			"code": "egress_status_unavailable", "detail": err.Error(),
		})
		return
	}
	exits := make([]ExitStatus, 0, len(snapshot.Exits))
	for country, exit := range snapshot.Exits {
		_, testErr := snapshot.ProxyURL(country)
		exits = append(exits, ExitStatus{
			Country: country, Ready: exit.Ready, Mode: exit.Mode, Node: exit.Node,
			CandidateCount: exit.CandidateCount, Error: exit.Error, Testable: testErr == nil,
		})
	}
	sort.Slice(exits, func(left, right int) bool { return exits[left].Country < exits[right].Country })
	writeJSON(response, http.StatusOK, map[string]any{
		"schema_version": 1, "layer": "country_egress", "exits": exits,
	})
}

func (handler *Handler) test(response http.ResponseWriter, request *http.Request) {
	country := strings.ToLower(strings.TrimSpace(request.PathValue("country")))
	if len(country) != 2 || country[0] < 'a' || country[0] > 'z' || country[1] < 'a' || country[1] > 'z' {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_egress_country"})
		return
	}
	snapshot, err := egressstatus.Load(handler.statusPath)
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{
			"code": "egress_status_unavailable", "detail": err.Error(),
		})
		return
	}
	proxyURL, err := snapshot.ProxyURL(country)
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{
			"code": "egress_not_testable", "detail": err.Error(), "layer": "country_egress",
		})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.probe(ctx, proxyURL)
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]any{
			"code": "egress_udp_probe_failed", "detail": err.Error(),
			"layer": "country_egress_udp", "country": country,
		})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"ok": true, "schema_version": 1, "layer": "country_egress_udp",
		"country": country, "latency_ms": result.LatencyMS, "target": result.Target,
		"attempted_targets": result.AttemptedTargets,
	})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
