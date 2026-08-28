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

type Handler struct{ manager *Manager }

func NewHandler(manager *Manager) (*Handler, error) {
	if manager == nil {
		return nil, errors.New("administrator auth manager is required")
	}
	return &Handler{manager: manager}, nil
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
	default:
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "auth_route_not_found"})
	}
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
