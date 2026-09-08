package provideradmin

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const WebPath = "/v1/system/web"

// These are the existing Core startup fields, not live listener changes.
type WebSettings struct {
	Listen  string `json:"listen"`
	TLSCert string `json:"tls_cert"`
	TLSKey  string `json:"tls_key"`
}

func (value WebSettings) Validate() error {
	host, portText, err := net.SplitHostPort(value.Listen)
	port, portErr := strconv.Atoi(portText)
	if err != nil || portErr != nil || port < 1 || port > 65535 || strings.TrimSpace(host) != host || strings.ContainsAny(host, "\r\n\t\x00 /") || len(host) > 255 ||
		!filepath.IsAbs(value.TLSCert) || !filepath.IsAbs(value.TLSKey) || len(value.TLSCert) > 4096 || len(value.TLSKey) > 4096 {
		return errors.New("invalid Web startup settings")
	}
	return nil
}

type WebSnapshot struct {
	SchemaVersion   int          `json:"schema_version"`
	Revision        string       `json:"revision"`
	Settings        WebSettings  `json:"settings"`
	Active          *WebSettings `json:"active,omitempty"`
	RestartRequired bool         `json:"restart_required"`
}

type WebRequest struct {
	SchemaVersion    int         `json:"schema_version"`
	ExpectedRevision string      `json:"expected_revision"`
	Settings         WebSettings `json:"settings"`
}

type WebService interface {
	WebSettings(context.Context) (WebSnapshot, error)
	SaveWebSettings(context.Context, WebRequest) (WebSnapshot, error)
}

type WebHandler struct {
	service WebService
	active  *WebSettings
}

func NewWebHandler(service WebService, active *WebSettings) (*WebHandler, error) {
	if service == nil {
		return nil, errors.New("Web settings service is required")
	}
	if active != nil {
		copy := *active
		active = &copy
	}
	return &WebHandler{service: service, active: active}, nil
}

func (handler *WebHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		writeError(w, &Error{Status: 400, Code: "invalid_web_settings_request"})
		return
	}
	var result WebSnapshot
	var err error
	switch r.Method {
	case http.MethodGet:
		result, err = handler.service.WebSettings(r.Context())
	case http.MethodPut:
		var input WebRequest
		if decodeErr := decodeRequest(w, r, &input); decodeErr != nil {
			writeError(w, decodeErr)
			return
		}
		if input.SchemaVersion != 1 || len(input.ExpectedRevision) != 64 || input.Settings.Validate() != nil {
			writeError(w, &Error{Status: 400, Code: "invalid_web_settings_request"})
			return
		}
		// The public Core checks with its own service-user file permissions too.
		if _, readErr := tls.LoadX509KeyPair(input.Settings.TLSCert, input.Settings.TLSKey); readErr != nil {
			writeError(w, &Error{Status: 400, Code: "web_tls_pair_unreadable_or_invalid"})
			return
		}
		if handler.active != nil && input.Settings.Listen != handler.active.Listen {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			listener, listenErr := (&net.ListenConfig{}).Listen(ctx, "tcp", input.Settings.Listen)
			cancel()
			if listenErr != nil {
				writeError(w, &Error{Status: 400, Code: "web_listen_unavailable"})
				return
			}
			_ = listener.Close()
		}
		result, err = handler.service.SaveWebSettings(r.Context(), input)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, &Error{Status: 405, Code: "method_not_allowed"})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result.Active = handler.active
	result.RestartRequired = handler.active != nil && *handler.active != result.Settings
	writeJSON(w, http.StatusOK, result)
}

func (client *Client) WebSettings(ctx context.Context) (WebSnapshot, error) {
	var result WebSnapshot
	err := client.requestPath(ctx, http.MethodGet, WebPath, nil, &result)
	return result, err
}
func (client *Client) SaveWebSettings(ctx context.Context, input WebRequest) (WebSnapshot, error) {
	var result WebSnapshot
	err := client.requestPath(ctx, http.MethodPut, WebPath, input, &result)
	return result, err
}
