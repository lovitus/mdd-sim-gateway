package egressconfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	maximumRequestBytes  = 1 << 20
	SnapshotIPCPath      = "/v1/internal/egress/config"
	maximumSnapshotBytes = 1 << 20
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if handler == nil || handler.store == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "egress_config_unavailable"})
		return
	}
	switch request.Method {
	case http.MethodGet:
		snapshot, err := handler.store.Snapshot()
		if err != nil {
			writeStoreError(response, err)
			return
		}
		response.Header().Set("ETag", revisionETag(snapshot.Revision))
		writeJSON(response, http.StatusOK, snapshot)
	case http.MethodPut:
		handler.put(response, request)
	default:
		response.Header().Set("Allow", "GET, PUT")
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
	}
}

func (handler *Handler) put(response http.ResponseWriter, request *http.Request) {
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "egress_config_revision_required"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumRequestBytes {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_egress_config"})
		return
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_egress_config"})
		return
	}
	snapshot, err := handler.store.PutExpected(config, expected)
	if errors.Is(err, ErrRevision) {
		if current, currentErr := handler.store.Snapshot(); currentErr == nil {
			response.Header().Set("ETag", revisionETag(current.Revision))
		}
		writeJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "egress_config_revision_changed"})
		return
	}
	if err != nil {
		writeStoreError(response, err)
		return
	}
	response.Header().Set("ETag", revisionETag(snapshot.Revision))
	writeJSON(response, http.StatusOK, snapshot)
}

type SnapshotHandler struct {
	store    *Store
	expected [32]byte
}

func NewSnapshotHandler(store *Store, token string) (*SnapshotHandler, error) {
	token = strings.TrimSpace(token)
	if store == nil || len(token) < 32 {
		return nil, errors.New("country exit snapshot handler configuration is invalid")
	}
	return &SnapshotHandler{store: store, expected: sha256.Sum256([]byte(token))}, nil
}

func (handler *SnapshotHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET")
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	header := request.Header.Get("Authorization")
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	if !strings.HasPrefix(header, "Bearer ") || subtle.ConstantTimeCompare(handler.expected[:], presented[:]) != 1 {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"code": "egress_config_snapshot_authentication_required"})
		return
	}
	snapshot, err := handler.store.Snapshot()
	if err != nil {
		writeStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, snapshot)
}

func FetchSnapshot(ctx context.Context, url, token string, client *http.Client) (Snapshot, error) {
	var snapshot Snapshot
	if ctx == nil || strings.TrimSpace(url) == "" || len(strings.TrimSpace(token)) < 32 {
		return snapshot, errors.New("invalid country exit snapshot request")
	}
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return snapshot, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return snapshot, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumSnapshotBytes))
		return snapshot, errors.New("country exit snapshot request failed")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumSnapshotBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumSnapshotBytes {
		return Snapshot{}, errors.New("country exit snapshot response is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		snapshot.SchemaVersion != SchemaVersion || snapshot.Revision == 0 || snapshot.Config.normalizeAndValidate() != nil {
		return Snapshot{}, errors.New("country exit snapshot response is invalid")
	}
	snapshot.Config = cloneConfig(snapshot.Config)
	return snapshot, nil
}

func revisionETag(revision uint64) string { return `"` + strconv.FormatUint(revision, 10) + `"` }

func parseIfMatch(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value[1:len(value)-1], `"`) {
		return 0, errors.New("invalid If-Match")
	}
	return strconv.ParseUint(value[1:len(value)-1], 10, 64)
}

func writeStoreError(response http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotImported) {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "egress_config_not_imported"})
		return
	}
	if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "required") ||
		strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "too many") ||
		strings.Contains(err.Error(), "too large") || strings.Contains(err.Error(), "unknown") {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_egress_config", "detail": err.Error()})
		return
	}
	writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "egress_config_store_failed"})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
