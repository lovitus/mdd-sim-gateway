package mediaauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
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
	recovery  *callhistory.Store
	probeHTTP *http.Client
}

func NewLeaseHandler(router *Router, providers *ProviderDirectory, auth BrowserMutationAuthorizer, ttl time.Duration, recovery ...*callhistory.Store) (*LeaseHandler, error) {
	if router == nil || providers == nil || auth == nil || ttl < 10*time.Second || ttl > 24*time.Hour {
		return nil, errors.New("invalid browser media lease handler configuration")
	}
	handler := &LeaseHandler{router: router, providers: providers, auth: auth, ttl: ttl, probeHTTP: &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConns: 16, IdleConnTimeout: 30 * time.Second}}}
	if len(recovery) > 1 {
		return nil, errors.New("multiple call recovery stores")
	}
	if len(recovery) == 1 {
		handler.recovery = recovery[0]
	}
	return handler, nil
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
		LineID      string `json:"line_id"`
		CallID      string `json:"call_id"`
		OperationID string `json:"operation_id,omitempty"`
		RecoveryKey string `json:"recovery_key,omitempty"`
	}
	if decodeLeaseRequest(request.Body, &input) != nil || !validID(strings.TrimSpace(input.LineID)) || !validID(strings.TrimSpace(input.CallID)) {
		writeLeaseJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_media_lease"})
		return
	}
	if input.RecoveryKey != "" && (handler.recovery == nil || !validID(input.OperationID) || len(input.RecoveryKey) < 64 || len(input.RecoveryKey) > 256) {
		writeLeaseJSON(response, 400, map[string]string{"code": "invalid_call_recovery"})
		return
	}
	generation, found := handler.providers.CurrentGeneration(input.LineID)
	if !found {
		writeLeaseJSON(response, http.StatusConflict, map[string]string{"code": "media_provider_unavailable"})
		return
	}
	if input.RecoveryKey != "" {
		provider, present := handler.providers.CurrentProvider(input.LineID)
		supported := false
		probeErr := errors.New("provider recovery capability unavailable")
		if present && provider.Generation == generation {
			parsed, parseErr := url.Parse(provider.BaseURL)
			if parseErr == nil && parsed.Scheme == "ws" {
				parsed.Scheme = "http"
				client, clientErr := vowifiipc.NewClient(parsed.String(), provider.Token, handler.probeHTTP)
				if clientErr == nil {
					ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
					supported, probeErr = client.SupportsCallReceipts(ctx)
					cancel()
				}
			}
		}
		if probeErr != nil {
			writeLeaseJSON(response, 503, map[string]string{"code": "provider_recovery_probe_failed"})
			return
		}
		if !supported {
			writeLeaseJSON(response, 409, map[string]string{"code": "provider_recovery_upgrade_required"})
			return
		}
	}
	lease, err := handler.router.Issue(LeaseRequest{
		Subject: subject, LineID: input.LineID, CallID: input.CallID,
		ProviderGeneration: generation, ExpiresAt: time.Now().UTC().Add(handler.ttl),
	})
	if err != nil {
		writeLeaseJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "media_lease_unavailable"})
		return
	}
	if input.RecoveryKey != "" {
		provider, found := handler.providers.CurrentProvider(input.LineID)
		if !found || provider.Generation != generation {
			handler.router.Revoke(lease.SessionID)
			writeLeaseJSON(response, 409, map[string]string{"code": "media_provider_changed"})
			return
		}
		err := handler.recovery.BindRecovery(callhistory.RecoveryRecord{LineID: input.LineID, Transport: "vowifi", CardID: provider.CardID, CallID: input.CallID, OperationID: input.OperationID, SessionID: lease.SessionID, Subject: subject, ProviderID: provider.ProviderID, ProviderGeneration: provider.Generation, CreatedAt: time.Now().UTC()}, input.RecoveryKey)
		if err != nil {
			handler.router.Revoke(lease.SessionID)
			writeLeaseJSON(response, 503, map[string]string{"code": "call_recovery_persist_failed"})
			return
		}
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
