package systempreferences

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/updatenetwork"
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
		writeJSON(response, http.StatusOK, struct {
			Snapshot
			NewDeviceDefaultsSupported bool `json:"new_device_defaults_supported"`
		}{snapshot, true})
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
		Retry             *recovery.ContinuousRetry `json:"retry"`
		NewDeviceDefaults *struct {
			ConnectionEnabled *bool `json:"connection_enabled"`
			VoWiFiEnabled     *bool `json:"vowifi_enabled"`
			FlightMode        *bool `json:"flight_mode"`
			RoamingEnabled    *bool `json:"roaming_enabled"`
		} `json:"new_device_defaults"`
		Updates            *updatenetwork.Selection `json:"updates"`
		AuditEnabled       *bool                    `json:"audit_enabled"`
		TrustedProxies     *[]string                `json:"trusted_proxies"`
		CallAudioBufferMS  *int                     `json:"call_audio_buffer_ms"`
		RingTimeoutSeconds *int                     `json:"ring_timeout_seconds"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&patch) != nil || decoder.Decode(&struct{}{}) != io.EOF || (patch.CallAudioBufferMS == nil && patch.RingTimeoutSeconds == nil && patch.AuditEnabled == nil && patch.TrustedProxies == nil && patch.Updates == nil && patch.NewDeviceDefaults == nil && patch.Retry == nil) {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_system_preferences"})
		return
	}
	current, err := handler.store.Snapshot()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "system_preferences_unavailable"})
		return
	}
	if patch.CallAudioBufferMS != nil {
		current.Preferences.CallAudioBufferMS = *patch.CallAudioBufferMS
	}
	if defaults := patch.NewDeviceDefaults; defaults != nil {
		if defaults.ConnectionEnabled == nil && defaults.VoWiFiEnabled == nil && defaults.FlightMode == nil && defaults.RoamingEnabled == nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_system_preferences"})
			return
		}
		// ec620942 device_state.py:set_defaults merges edits with the original
		// future-device baseline; absence alone still does not publish an intent.
		value := NewDeviceDefaults{VoWiFiEnabled: true}
		if current.Preferences.NewDeviceDefaults != nil {
			value = *current.Preferences.NewDeviceDefaults
		}
		if defaults.ConnectionEnabled != nil {
			value.ConnectionEnabled = *defaults.ConnectionEnabled
		}
		if defaults.VoWiFiEnabled != nil {
			value.VoWiFiEnabled = *defaults.VoWiFiEnabled
		}
		if defaults.FlightMode != nil {
			value.FlightMode = *defaults.FlightMode
		}
		if defaults.RoamingEnabled != nil {
			value.RoamingEnabled = *defaults.RoamingEnabled
		}
		current.Preferences.NewDeviceDefaults = &value
	}
	if patch.AuditEnabled != nil {
		current.Preferences.AuditEnabled = patch.AuditEnabled
	}
	if patch.Updates != nil {
		current.Preferences.Updates = patch.Updates
	}
	if patch.Retry != nil {
		current.Preferences.Retry = patch.Retry
	}
	if patch.TrustedProxies != nil {
		current.Preferences.TrustedProxies = *patch.TrustedProxies
	}
	if patch.RingTimeoutSeconds != nil {
		if *patch.RingTimeoutSeconds < 5 || *patch.RingTimeoutSeconds > 180 {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_system_preferences"})
			return
		}
		current.Preferences.RingTimeoutSeconds = *patch.RingTimeoutSeconds
	}
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
