package egressprofiletest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressexec"
)

type Store interface {
	Snapshot() (egressconfig.Snapshot, error)
}

type Prober func(context.Context, string, string, egressconfig.Profile) (egressexec.ProfileProbeResult, error)

type Handler struct {
	store   Store
	binary  string
	root    string
	probe   Prober
	mu      sync.Mutex
	working map[string]struct{}
}

func NewHandler(store Store, binary, root string) (*Handler, error) {
	if store == nil || !filepath.IsAbs(binary) || !filepath.IsAbs(root) {
		return nil, errors.New("invalid egress profile test configuration")
	}
	return &Handler{store: store, binary: binary, root: root, probe: egressexec.ProbeProfile,
		working: map[string]struct{}{}}, nil
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
	if err != nil || !found || (profile.Type != "node" && profile.Type != "socks5") {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]string{"code": "egress_profile_not_testable"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 12*time.Second)
	result, err := handler.probe(ctx, handler.binary, handler.root, profile)
	cancel()
	if err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]string{
			"code": "egress_profile_udp_probe_failed", "detail": bounded(err.Error()),
		})
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
