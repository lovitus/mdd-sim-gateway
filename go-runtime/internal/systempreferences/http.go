package systempreferences

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maximumBodyBytes = 4096

type Handler struct{ store *Store }

func NewHandler(store *Store) (*Handler, error) {
	if store == nil {
		return nil, errors.New("system preference store is required")
	}
	return &Handler{store: store}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.URL.RawQuery != "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_system_preferences_request"})
		return
	}
	switch request.Method {
	case http.MethodGet:
		snapshot, err := handler.store.Snapshot()
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "system_preferences_unavailable"})
			return
		}
		response.Header().Set("ETag", etag(snapshot.Revision))
		writeJSON(response, http.StatusOK, snapshot)
	case http.MethodPatch:
		handler.patch(response, request)
	default:
		response.Header().Set("Allow", "GET, PATCH")
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
	}
}

func (handler *Handler) patch(response http.ResponseWriter, request *http.Request) {
	expected, err := parseETag(request.Header.Get("If-Match"))
	if err != nil {
		writeJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "system_preferences_revision_required"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumBodyBytes {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_system_preferences"})
		return
	}
	var patch struct {
		CallAudioBufferMS *int `json:"call_audio_buffer_ms"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&patch) != nil || decoder.Decode(&struct{}{}) != io.EOF || patch.CallAudioBufferMS == nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_system_preferences"})
		return
	}
	current, err := handler.store.Snapshot()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "system_preferences_unavailable"})
		return
	}
	current.Preferences.CallAudioBufferMS = *patch.CallAudioBufferMS
	updated, err := handler.store.PutExpected(current.Preferences, expected)
	if errors.Is(err, ErrRevision) {
		if latest, latestErr := handler.store.Snapshot(); latestErr == nil {
			response.Header().Set("ETag", etag(latest.Revision))
		}
		writeJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "system_preferences_revision_changed"})
		return
	}
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_system_preferences"})
		return
	}
	response.Header().Set("ETag", etag(updated.Revision))
	writeJSON(response, http.StatusOK, updated)
}

func etag(revision uint64) string { return `"` + strconv.FormatUint(revision, 10) + `"` }

func parseETag(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("invalid ETag")
	}
	return strconv.ParseUint(value[1:len(value)-1], 10, 64)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
