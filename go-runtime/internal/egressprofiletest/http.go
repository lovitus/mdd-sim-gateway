package egressprofiletest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressexec"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressprobe"
)

type Store interface {
	Snapshot() (egressconfig.Snapshot, error)
}

type Prober func(context.Context, string, string, egressconfig.Profile) (egressexec.ProfileProbeResult, error)
type CellularProber func(context.Context, string) (egressprobe.Result, error)

type Handler struct {
	store    Store
	binary   string
	root     string
	probe    Prober
	cellular CellularProber
	mu       sync.Mutex
	working  map[string]struct{}
}

func NewHandler(store Store, binary, root string, cellular ...CellularProber) (*Handler, error) {
	if store == nil || !filepath.IsAbs(binary) || !filepath.IsAbs(root) {
		return nil, errors.New("invalid egress profile test configuration")
	}
	if len(cellular) > 1 {
		return nil, errors.New("multiple cellular profile probes")
	}
	handler := &Handler{store: store, binary: binary, root: root, probe: egressexec.ProbeProfile,
		working: map[string]struct{}{}}
	if len(cellular) == 1 {
		handler.cellular = cellular[0]
	}
	return handler, nil
}

func NewHandlerWithXray(store Store, binary, xray, root string, cellular ...CellularProber) (*Handler, error) {
	if !filepath.IsAbs(xray) || filepath.Clean(xray) == string(filepath.Separator) {
		return nil, errors.New("invalid Xray profile test path")
	}
	handler, err := NewHandler(store, binary, root, cellular...)
	if err != nil {
		return nil, err
	}
	handler.probe = func(ctx context.Context, binary, root string, profile egressconfig.Profile) (egressexec.ProfileProbeResult, error) {
		return egressexec.ProbeProfileWithXray(ctx, binary, xray, root, profile)
	}
	return handler, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	profileID := strings.TrimSpace(request.PathValue("profileID"))
	if request.Method != http.MethodPost || request.URL.RawQuery != "" || profileID == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_egress_profile_test"})
		return
	}
	handler.mu.Lock()
	if _, busy := handler.working[profileID]; busy {
		handler.mu.Unlock()
		writeJSON(response, http.StatusConflict, map[string]string{"code": "egress_profile_test_active"})
		return
	}
	handler.working[profileID] = struct{}{}
	handler.mu.Unlock()
	defer func() {
		handler.mu.Lock()
		delete(handler.working, profileID)
		handler.mu.Unlock()
	}()
	snapshot, err := handler.store.Snapshot()
	expected, revisionErr := parseRevision(request.Header.Get("If-Match"))
	if revisionErr != nil {
		writeJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "egress_profile_test_revision_required"})
		return
	}
	if err == nil && snapshot.Revision != expected {
		writeJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "egress_profile_test_revision_changed"})
		return
	}
	profile, found := snapshot.Config.Profiles[profileID]
	var input struct {
		Profile *egressconfig.Profile `json:"profile,omitempty"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && err != io.EOF {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_egress_profile_test"})
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_egress_profile_test"})
		return
	}
	if input.Profile != nil {
		profile, found = *input.Profile, true
	}
	if err != nil || !found || (profile.Type != "node" && profile.Type != "socks5" && profile.Type != "cellular_sim") {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]string{"code": "egress_profile_not_testable"})
		return
	}
	budget := 12 * time.Second
	if profile.Type == "cellular_sim" {
		budget = time.Minute
	}
	ctx, cancel := context.WithTimeout(request.Context(), budget)
	var result egressexec.ProfileProbeResult
	if profile.Type == "cellular_sim" {
		if handler.cellular == nil {
			err = errors.New("cellular data probe executor unavailable")
		} else {
			var probe egressprobe.Result
			probe, err = handler.cellular(ctx, profile.SIMICCID)
			result = egressexec.ProfileProbeResult{Node: "cellular_sim", LatencyMS: probe.LatencyMS, Target: probe.Target, AttemptedTargets: probe.AttemptedTargets}
		}
	} else {
		result, err = handler.probe(ctx, handler.binary, handler.root, profile)
	}
	cancel()
	if err != nil {
		code := "egress_profile_udp_probe_failed"
		if profile.Type == "cellular_sim" {
			code = "cellular_data_probe_failed"
		}
		failure := map[string]string{
			"code": code, "detail": bounded(err.Error()),
		}
		var remote *agentlink.RemoteError
		if errors.As(err, &remote) && remote != nil {
			failure["cause_code"] = remote.Code
			failure["cause_kind"] = remote.Kind
			failure["layer"] = "agent"
		}
		writeJSON(response, http.StatusBadGateway, failure)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"profile_id": profileID, "config_revision": snapshot.Revision, "result": result})
}

func parseRevision(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("invalid revision")
	}
	return strconv.ParseUint(value[1:len(value)-1], 10, 64)
}

func bounded(value string) string {
	value = strings.ToValidUTF8(value, "?")
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
