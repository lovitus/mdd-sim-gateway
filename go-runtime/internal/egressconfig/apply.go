package egressconfig

import (
	"bytes"
	"context"
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
	ApplyPath        = "/v1/system/egress-config"
	applyTokenHeader = "X-MDD-Provider-Apply-Token"
)

type ApplyStatus struct {
	SchemaVersion     int    `json:"schema_version"`
	ConfigRevision    uint64 `json:"config_revision"`
	CatalogRevision   uint64 `json:"catalog_revision"`
	AppliedConfig     uint64 `json:"applied_config_revision"`
	AppliedCatalog    uint64 `json:"applied_catalog_revision"`
	DesiredGeneration string `json:"desired_generation,omitempty"`
	RuntimeConfirmed  bool   `json:"runtime_confirmed"`
	Pending           bool   `json:"pending"`
	Applying          bool   `json:"applying"`
}

type ApplyRequest struct {
	SchemaVersion   int    `json:"schema_version"`
	ConfigRevision  uint64 `json:"config_revision"`
	CatalogRevision uint64 `json:"catalog_revision"`
}

type ApplyResult struct {
	SchemaVersion   int    `json:"schema_version"`
	ConfigRevision  uint64 `json:"config_revision"`
	CatalogRevision uint64 `json:"catalog_revision"`
	Generation      string `json:"generation"`
	State           string `json:"state"`
	Code            string `json:"code"`
}

type ApplyService interface {
	EgressStatus(context.Context) (ApplyStatus, error)
	ApplyEgress(context.Context, uint64, uint64) (ApplyResult, error)
}

type ApplyError struct {
	Status int
	Code   string
	Detail string
	Cause  error
}

func (failure *ApplyError) Error() string {
	if failure == nil {
		return "country exit apply failed"
	}
	if failure.Cause != nil {
		return failure.Cause.Error()
	}
	return failure.Code
}

func (failure *ApplyError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Cause
}

type ApplyHandler struct{ service ApplyService }

func NewApplyHandler(service ApplyService) (*ApplyHandler, error) {
	if service == nil {
		return nil, errors.New("country exit apply service is required")
	}
	return &ApplyHandler{service: service}, nil
}

func (handler *ApplyHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	switch request.Method {
	case http.MethodGet:
		status, err := handler.service.EgressStatus(request.Context())
		if err != nil {
			writeApplyError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, status)
	case http.MethodPost:
		var input ApplyRequest
		if err := decodeApplyRequest(response, request, &input); err != nil {
			writeApplyError(response, err)
			return
		}
		if input.SchemaVersion != SchemaVersion || input.ConfigRevision == 0 || input.CatalogRevision == 0 {
			writeApplyError(response, &ApplyError{Status: http.StatusBadRequest, Code: "invalid_egress_apply_request"})
			return
		}
		result, err := handler.service.ApplyEgress(request.Context(), input.ConfigRevision, input.CatalogRevision)
		if err != nil {
			writeApplyError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
	default:
		response.Header().Set("Allow", "GET, POST")
		writeApplyError(response, &ApplyError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed"})
	}
}

type ApplyClient struct {
	token string
	http  *http.Client
}

func NewApplyClient(socketPath, token string) (*ApplyClient, error) {
	socketPath, token = filepath.Clean(strings.TrimSpace(socketPath)), strings.TrimSpace(token)
	if !filepath.IsAbs(socketPath) || socketPath == string(filepath.Separator) || len(token) < 32 {
		return nil, errors.New("country exit apply client configuration is invalid")
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	return &ApplyClient{token: token, http: &http.Client{
		Transport: transport, Timeout: 45 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("country exit apply redirect refused") },
	}}, nil
}

func (client *ApplyClient) EgressStatus(ctx context.Context) (ApplyStatus, error) {
	var result ApplyStatus
	err := client.request(ctx, http.MethodGet, nil, &result)
	return result, err
}

func (client *ApplyClient) ApplyEgress(ctx context.Context, configRevision, catalogRevision uint64) (ApplyResult, error) {
	var result ApplyResult
	err := client.request(ctx, http.MethodPost, ApplyRequest{
		SchemaVersion: SchemaVersion, ConfigRevision: configRevision, CatalogRevision: catalogRevision,
	}, &result)
	return result, err
}

func (client *ApplyClient) request(ctx context.Context, method string, input, output any) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://mdd-provider-apply"+ApplyPath, body)
	if err != nil {
		return err
	}
	request.Header.Set(applyTokenHeader, client.token)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return &ApplyError{Status: http.StatusServiceUnavailable, Code: "egress_apply_unavailable", Cause: err}
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumRequestBytes+1))
	if err != nil || len(payload) > maximumRequestBytes {
		return &ApplyError{Status: http.StatusBadGateway, Code: "egress_apply_response_invalid", Cause: err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Code   string `json:"code"`
			Detail string `json:"detail,omitempty"`
		}
		if json.Unmarshal(payload, &failure) != nil || strings.TrimSpace(failure.Code) == "" {
			return &ApplyError{Status: http.StatusBadGateway, Code: "egress_apply_response_invalid"}
		}
		return &ApplyError{Status: response.StatusCode, Code: failure.Code, Detail: failure.Detail}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return &ApplyError{Status: http.StatusBadGateway, Code: "egress_apply_response_invalid"}
	}
	return nil
}

func decodeApplyRequest(response http.ResponseWriter, request *http.Request, output any) error {
	request.Body = http.MaxBytesReader(response, request.Body, maximumRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return &ApplyError{Status: http.StatusBadRequest, Code: "invalid_egress_apply_request"}
	}
	return nil
}

func writeApplyError(response http.ResponseWriter, err error) {
	status, code, detail := http.StatusInternalServerError, "egress_apply_failed", ""
	var failure *ApplyError
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
