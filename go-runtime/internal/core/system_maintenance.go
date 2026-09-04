package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

const maximumSystemMaintenanceBytes = 64 << 10

type MaintenanceRuntime interface {
	Request(context.Context, providerapply.DrainRequest, bool) (providerapply.DrainResult, error)
}

type MaintenanceStatusRuntime interface {
	Snapshot(context.Context) (providerapply.Snapshot, error)
}

type SystemMaintenanceHandler struct{ runtime MaintenanceRuntime }

func NewSystemMaintenanceHandler(runtime MaintenanceRuntime) (*SystemMaintenanceHandler, error) {
	if runtime == nil {
		return nil, errors.New("system maintenance runtime is required")
	}
	return &SystemMaintenanceHandler{runtime: runtime}, nil
}

func (handler *SystemMaintenanceHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodGet {
		statusRuntime, ok := handler.runtime.(MaintenanceStatusRuntime)
		if !ok {
			writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "maintenance_status_unavailable"})
			return
		}
		snapshot, err := statusRuntime.Snapshot(request.Context())
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "maintenance_status_unavailable"})
			return
		}
		writeJSON(response, http.StatusOK, snapshot)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumSystemMaintenanceBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumSystemMaintenanceBytes {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_maintenance_request"})
		return
	}
	var input struct {
		Action  string                     `json:"action"`
		Request providerapply.DrainRequest `json:"request"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || (strings.TrimSpace(input.Action) != "begin" && strings.TrimSpace(input.Action) != "resume") {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_maintenance_request"})
		return
	}
	result, err := handler.runtime.Request(request.Context(), input.Request, input.Action == "begin")
	if err != nil {
		writeJSON(response, http.StatusConflict, result)
		return
	}
	writeJSON(response, http.StatusOK, result)
}
