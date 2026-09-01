package systemstatus

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type Handler struct {
	sampler *Sampler
	now     func() time.Time
}

func NewHandler(sampler *Sampler, now func() time.Time) (*Handler, error) {
	if sampler == nil {
		return nil, errors.New("system status sampler is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Handler{sampler: sampler, now: now}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodGet {
		response.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(response).Encode(map[string]string{"code": "method_not_allowed"})
		return
	}
	if request.URL.RawQuery != "" {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]string{"code": "invalid_system_status_request"})
		return
	}
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(handler.sampler.Snapshot(handler.now().UTC()))
}
