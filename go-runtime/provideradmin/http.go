// Package provideradmin exposes the narrow, typed boundary between the
// unprivileged public Core and the privileged provider configuration applier.
package provideradmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const (
	Path             = "/v1/system/provider-config"
	SchemaVersion    = 1
	maximumBodyBytes = 1 << 20
	localTokenHeader = "X-MDD-Provider-Apply-Token"
)

type Status struct {
	SchemaVersion   int    `json:"schema_version"`
	CatalogRevision uint64 `json:"catalog_revision"`
	AppliedRevision uint64 `json:"applied_revision"`
	Pending         bool   `json:"pending"`
	Applying        bool   `json:"applying"`
	LastApplyID     string `json:"last_apply_id,omitempty"`
	LastState       string `json:"last_state,omitempty"`
	LastCode        string `json:"last_code,omitempty"`
}

type ApplyRequest struct {
	SchemaVersion   int    `json:"schema_version"`
	CatalogRevision uint64 `json:"catalog_revision"`
}

type ApplyResult struct {
	SchemaVersion   int    `json:"schema_version"`
	CatalogRevision uint64 `json:"catalog_revision"`
	ApplyID         string `json:"apply_id,omitempty"`
	State           string `json:"state"`
	Code            string `json:"code,omitempty"`
	Added           int    `json:"added"`
	Changed         int    `json:"changed"`
	Removed         int    `json:"removed"`
}

type Service interface {
	Status(context.Context) (Status, error)
	Apply(context.Context, uint64) (ApplyResult, error)
}

type Error struct {
	Status int
	Code   string
	Detail string
	Cause  error
}

func (failure *Error) Error() string {
	if failure == nil {
		return "provider configuration request failed"
	}
	if failure.Cause != nil {
		return failure.Cause.Error()
	}
	return failure.Code
}

func (failure *Error) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Cause
}

type Handler struct{ service Service }

func NewHandler(service Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("provider configuration service is required")
	}
	return &Handler{service: service}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	switch request.Method {
	case http.MethodGet:
		status, err := handler.service.Status(request.Context())
		if err != nil {
			writeError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, status)
	case http.MethodPost:
		var input ApplyRequest
		if err := decodeRequest(response, request, &input); err != nil {
			writeError(response, err)
			return
		}
		if input.SchemaVersion != SchemaVersion || input.CatalogRevision == 0 {
			writeError(response, &Error{Status: http.StatusBadRequest, Code: "invalid_apply_request"})
			return
		}
		result, err := handler.service.Apply(request.Context(), input.CatalogRevision)
		if err != nil {
			writeError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
	default:
		response.Header().Set("Allow", "GET, POST")
		writeError(response, &Error{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed"})
	}
}

func Authenticate(next http.Handler, token string) (http.Handler, error) {
	token = strings.TrimSpace(token)
	if next == nil || len(token) < 32 {
		return nil, errors.New("provider apply authentication input is invalid")
	}
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		actual := sha256.Sum256([]byte(strings.TrimSpace(request.Header.Get(localTokenHeader))))
		if subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			writeError(response, &Error{Status: http.StatusUnauthorized, Code: "provider_apply_authentication_required"})
			return
		}
		next.ServeHTTP(response, request)
	}), nil
}

type Client struct {
	socketPath string
	token      string
	http       *http.Client
}

func NewClient(socketPath, token string) (*Client, error) {
	socketPath, token = filepath.Clean(strings.TrimSpace(socketPath)), strings.TrimSpace(token)
	if !filepath.IsAbs(socketPath) || socketPath == string(filepath.Separator) || len(token) < 32 {
		return nil, errors.New("provider apply client configuration is invalid")
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		socketPath: socketPath,
		token:      token,
		http: &http.Client{
			Transport: transport,
			Timeout:   130 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("provider apply redirect refused")
			},
		},
	}, nil
}

func (client *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	err := client.request(ctx, http.MethodGet, nil, &status)
	return status, err
}

func (client *Client) Apply(ctx context.Context, revision uint64) (ApplyResult, error) {
	var result ApplyResult
	err := client.request(ctx, http.MethodPost, ApplyRequest{SchemaVersion: SchemaVersion, CatalogRevision: revision}, &result)
	return result, err
}

func (client *Client) request(ctx context.Context, method string, input, output any) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://mdd-provider-apply"+Path, body)
	if err != nil {
		return err
	}
	request.Header.Set(localTokenHeader, client.token)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return &Error{Status: http.StatusServiceUnavailable, Code: "provider_apply_unavailable", Cause: err}
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumBodyBytes+1))
	if err != nil || len(payload) > maximumBodyBytes {
		return &Error{Status: http.StatusBadGateway, Code: "provider_apply_response_invalid", Cause: err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Code   string `json:"code"`
			Detail string `json:"detail,omitempty"`
		}
		if json.Unmarshal(payload, &failure) != nil || strings.TrimSpace(failure.Code) == "" {
			return &Error{Status: http.StatusBadGateway, Code: "provider_apply_response_invalid"}
		}
		return &Error{Status: response.StatusCode, Code: failure.Code, Detail: failure.Detail}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return &Error{Status: http.StatusBadGateway, Code: "provider_apply_response_invalid", Cause: err}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &Error{Status: http.StatusBadGateway, Code: "provider_apply_response_invalid"}
	}
	return nil
}

func decodeRequest(response http.ResponseWriter, request *http.Request, output any) error {
	request.Body = http.MaxBytesReader(response, request.Body, maximumBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return &Error{Status: http.StatusBadRequest, Code: "invalid_apply_request"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &Error{Status: http.StatusBadRequest, Code: "invalid_apply_request"}
	}
	return nil
}

func writeError(response http.ResponseWriter, err error) {
	status, code, detail := http.StatusInternalServerError, "provider_apply_failed", ""
	var failure *Error
	if errors.As(err, &failure) {
		if failure.Status >= 400 && failure.Status <= 599 {
			status = failure.Status
		}
		if strings.TrimSpace(failure.Code) != "" {
			code = failure.Code
		}
		detail = strings.TrimSpace(failure.Detail)
	}
	payload := map[string]string{"code": code}
	if detail != "" {
		payload["detail"] = detail
	}
	writeJSON(response, status, payload)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
