package adminauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct {
	manager    *Manager
	invalidate func(string)
}

type HandlerOption func(*Handler) error

func WithAgentCredentialInvalidator(invalidate func(string)) HandlerOption {
	return func(handler *Handler) error {
		if invalidate == nil {
			return errors.New("Agent credential invalidator is required")
		}
		handler.invalidate = invalidate
		return nil
	}
}

func NewHandler(manager *Manager, options ...HandlerOption) (*Handler, error) {
	if manager == nil {
		return nil, errors.New("administrator auth manager is required")
	}
	handler := &Handler{manager: manager}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("nil administrator auth handler option")
		}
		if err := option(handler); err != nil {
			return nil, err
		}
	}
	return handler, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	switch request.URL.Path {
	case "/api/auth/status":
		if request.Method != http.MethodGet {
			writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
			return
		}
		handler.status(response, request)
	case "/api/auth/login":
		if request.Method != http.MethodPost {
			writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
			return
		}
		handler.login(response, request)
	case "/api/auth/logout":
		if request.Method != http.MethodPost {
			writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
			return
		}
		handler.logout(response, request)
	case "/api/auth/password":
		if request.Method != http.MethodPost {
			writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
			return
		}
		handler.password(response, request)
	case "/api/auth/agent-token":
		if request.Method != http.MethodPost {
			writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
			return
		}
		handler.agentToken(response, request)
	case "/api/auth/agent-credentials":
		handler.agentCredentials(response, request)
	default:
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "auth_route_not_found"})
	}
}

func (handler *Handler) agentToken(response http.ResponseWriter, request *http.Request) {
	if _, err := handler.manager.Authorize(request, true); err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrCSRF) {
			status = http.StatusForbidden
		}
		writeJSON(response, status, map[string]string{"detail": "authentication or CSRF validation failed"})
		return
	}
	token, err := handler.manager.RotateAgentToken()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"detail": "Agent token rotation unavailable"})
		return
	}
	if handler.invalidate != nil {
		handler.invalidate("")
	}
	writeJSON(response, http.StatusOK, map[string]any{"ok": true, "agent_token": token, "agents_must_restart": true})
}

func (handler *Handler) agentCredentials(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	if _, err := handler.manager.Authorize(request, request.Method == http.MethodPost); err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrCSRF) {
			status = http.StatusForbidden
		}
		writeJSON(response, status, map[string]string{"detail": "authentication or CSRF validation failed"})
		return
	}
	if request.Method == http.MethodGet {
		writeJSON(response, http.StatusOK, handler.manager.AgentCredentials())
		return
	}
	var input struct {
		Action  string `json:"action"`
		AgentID string `json:"agent_id,omitempty"`
		Mode    string `json:"mode,omitempty"`
	}
	if err := decodeStrict(request, &input, 4096); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_agent_credential_request"})
		return
	}
	switch strings.TrimSpace(input.Action) {
	case "issue":
		if strings.TrimSpace(input.Mode) != "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_agent_credential_request"})
			return
		}
		token, err := handler.manager.IssueAgentToken(input.AgentID)
		if err != nil {
			handler.writeAgentCredentialError(response, err)
			return
		}
		if handler.invalidate != nil {
			handler.invalidate(strings.TrimSpace(input.AgentID))
		}
		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "agent_id": strings.TrimSpace(input.AgentID),
			"agent_token": token, "agent_must_restart": true, "credentials": handler.manager.AgentCredentials()})
	case "revoke":
		if strings.TrimSpace(input.Mode) != "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_agent_credential_request"})
			return
		}
		if err := handler.manager.RevokeAgentToken(input.AgentID); err != nil {
			handler.writeAgentCredentialError(response, err)
			return
		}
		if handler.invalidate != nil {
			handler.invalidate(strings.TrimSpace(input.AgentID))
		}
		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "agent_id": strings.TrimSpace(input.AgentID),
			"credentials": handler.manager.AgentCredentials()})
	case "unenroll":
		if strings.TrimSpace(input.Mode) != "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_agent_credential_request"})
			return
		}
		if err := handler.manager.UnenrollAgentToken(input.AgentID); err != nil {
			handler.writeAgentCredentialError(response, err)
			return
		}
		if handler.invalidate != nil {
			handler.invalidate(strings.TrimSpace(input.AgentID))
		}
		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "agent_id": strings.TrimSpace(input.AgentID),
			"credentials": handler.manager.AgentCredentials()})
	case "set_mode":
		if strings.TrimSpace(input.AgentID) != "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_agent_credential_request"})
			return
		}
		changed, err := handler.manager.SetAgentCredentialMode(input.Mode)
		if err != nil {
			handler.writeAgentCredentialError(response, err)
			return
		}
		if changed && handler.invalidate != nil {
			handler.invalidate("")
		}
		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "changed": changed,
			"credentials": handler.manager.AgentCredentials()})
	default:
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_agent_credential_request"})
	}
}

func (handler *Handler) writeAgentCredentialError(response http.ResponseWriter, err error) {
	if errors.Is(err, ErrAgentCredential) {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_agent_credential_request"})
		return
	}
	writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "agent_credential_update_unavailable"})
}

func (handler *Handler) password(response http.ResponseWriter, request *http.Request) {
	if _, err := handler.manager.Authorize(request, true); err != nil {
		status, detail := http.StatusUnauthorized, "authentication required"
		if errors.Is(err, ErrCSRF) {
			status, detail = http.StatusForbidden, "invalid CSRF token"
		}
		writeJSON(response, status, map[string]string{"detail": detail})
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeStrict(request, &input, 4096); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"detail": "invalid password request"})
		return
	}
	if err := handler.manager.ChangePassword(input.CurrentPassword, input.NewPassword); err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			writeJSON(response, http.StatusUnauthorized, map[string]string{"detail": "invalid current or new password"})
			return
		}
		writeJSON(response, http.StatusInternalServerError, map[string]string{"detail": "password update unavailable"})
		return
	}
	http.SetCookie(response, &http.Cookie{Name: SessionCookie, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: handler.manager.SecureCookies(), SameSite: http.SameSiteLaxMode})
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (handler *Handler) status(response http.ResponseWriter, request *http.Request) {
	token := TokenFromRequest(request)
	session, found := handler.manager.Session(token)
	result := map[string]any{"configured": true, "authenticated": found, "username": handler.manager.Username(), "token": "", "csrf": ""}
	if found {
		result["token"], result["csrf"] = token, session.CSRF
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *Handler) login(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeStrict(request, &input, 4096); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"detail": "invalid login request"})
		return
	}
	result, err := handler.manager.Login(input.Username, input.Password, remoteHost(request.RemoteAddr))
	if err != nil {
		var throttle *ThrottleError
		switch {
		case errors.As(err, &throttle):
			response.Header().Set("Retry-After", strconv.Itoa(throttle.RetryAfter))
			writeJSON(response, http.StatusTooManyRequests, map[string]any{"detail": "too many attempts", "retry_after": throttle.RetryAfter})
		case errors.Is(err, ErrInvalidCredentials):
			writeJSON(response, http.StatusUnauthorized, map[string]string{"detail": err.Error()})
		default:
			writeJSON(response, http.StatusServiceUnavailable, map[string]string{"detail": "authentication unavailable"})
		}
		return
	}
	http.SetCookie(response, &http.Cookie{Name: SessionCookie, Value: result.Token, Path: "/",
		MaxAge: int(SessionTTL.Seconds()), HttpOnly: true, Secure: handler.manager.SecureCookies(), SameSite: http.SameSiteLaxMode})
	writeJSON(response, http.StatusOK, map[string]any{"ok": true, "authenticated": true, "token": result.Token, "csrf": result.Session.CSRF})
}

func (handler *Handler) logout(response http.ResponseWriter, request *http.Request) {
	if _, err := handler.manager.Authorize(request, true); err != nil {
		status, detail := http.StatusUnauthorized, "authentication required"
		if errors.Is(err, ErrCSRF) {
			status, detail = http.StatusForbidden, "invalid CSRF token"
		}
		writeJSON(response, status, map[string]string{"detail": detail})
		return
	}
	handler.manager.Logout(TokenFromRequest(request))
	http.SetCookie(response, &http.Cookie{Name: SessionCookie, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: handler.manager.SecureCookies(), SameSite: http.SameSiteLaxMode})
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func decodeStrict(request *http.Request, target any, limit int64) error {
	payload, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil || len(payload) > int(limit) {
		return errors.New("request body is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request has trailing data")
	}
	return nil
}

func remoteHost(remote string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remote))
	if err != nil {
		return ""
	}
	return host
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
