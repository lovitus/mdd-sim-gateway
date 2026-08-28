package mediaauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const maximumLeaseRequestBytes = 4096

type BrowserMutationAuthorizer interface {
	AuthorizeBrowserMutation(*http.Request) (string, error)
}

type LeaseHandler struct {
	router    *Router
	providers *ProviderDirectory
	auth      BrowserMutationAuthorizer
	ttl       time.Duration
}

func NewLeaseHandler(router *Router, providers *ProviderDirectory, auth BrowserMutationAuthorizer, ttl time.Duration) (*LeaseHandler, error) {
	if router == nil || providers == nil || auth == nil || ttl < 10*time.Second || ttl > 24*time.Hour {
		return nil, errors.New("invalid browser media lease handler configuration")
	}
	return &LeaseHandler{router: router, providers: providers, auth: auth, ttl: ttl}, nil
}

func (handler *LeaseHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.Path != "/v1/media/leases" || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	subject, err := handler.auth.AuthorizeBrowserMutation(request)
	if err != nil {
		writeLeaseJSON(response, http.StatusForbidden, map[string]string{"code": "browser_authorization_failed"})
		return
	}
	switch request.Method {
	case http.MethodPost:
		handler.issue(response, request, subject)
	case http.MethodDelete:
		handler.revoke(response, request)
	default:
		writeLeaseJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
	}
}

func (handler *LeaseHandler) issue(response http.ResponseWriter, request *http.Request, subject string) {
	var input struct {
		LineID string `json:"line_id"`
		CallID string `json:"call_id"`
	}
	if decodeLeaseRequest(request.Body, &input) != nil || !validID(strings.TrimSpace(input.LineID)) || !validID(strings.TrimSpace(input.CallID)) {
		writeLeaseJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_media_lease"})
		return
	}
	generation, found := handler.providers.CurrentGeneration(input.LineID)
	if !found {
		writeLeaseJSON(response, http.StatusConflict, map[string]string{"code": "media_provider_unavailable"})
		return
	}
	lease, err := handler.router.Issue(LeaseRequest{
		Subject: subject, LineID: input.LineID, CallID: input.CallID,
		ProviderGeneration: generation, ExpiresAt: time.Now().UTC().Add(handler.ttl),
	})
	if err != nil {
		writeLeaseJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "media_lease_unavailable"})
		return
	}
	writeLeaseJSON(response, http.StatusCreated, map[string]any{
		"session_id": lease.SessionID,
		"ws_path":    "/api/browser-media/" + lease.SessionID + "/ws",
		"expires_at": lease.ExpiresAt,
	})
}

func (handler *LeaseHandler) revoke(response http.ResponseWriter, request *http.Request) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if decodeLeaseRequest(request.Body, &input) != nil || !validID(strings.TrimSpace(input.SessionID)) {
		writeLeaseJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_media_lease"})
		return
	}
	handler.router.Revoke(input.SessionID)
	response.WriteHeader(http.StatusNoContent)
}

func decodeLeaseRequest(body io.Reader, target any) error {
	payload, err := io.ReadAll(io.LimitReader(body, maximumLeaseRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumLeaseRequestBytes {
		return errors.New("invalid media lease request size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("media lease request has trailing JSON")
	}
	return nil
}

func writeLeaseJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
