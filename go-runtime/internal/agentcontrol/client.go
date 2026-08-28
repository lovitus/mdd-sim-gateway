package agentcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const maxControlResponseBytes = 1 << 20

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type APIError struct {
	Status int
	Code   string
}

func (err *APIError) Error() string {
	return fmt.Sprintf("Agent control request failed: HTTP %d (%s)", err.Status, err.Code)
}

func NewClient(baseURL, token string, client *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.Port() == "" ||
		len(token) < minimumControlTokenBytes {
		return nil, errors.New("invalid Agent control client configuration")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("Agent control client requires a literal loopback endpoint")
	}
	if client == nil {
		client = &http.Client{}
	}
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), token: token, http: client}, nil
}

func (client *Client) Status(ctx context.Context) (Snapshot, error) {
	return client.request(ctx, http.MethodGet, "/v1/status")
}

func (client *Client) Start(ctx context.Context) (Snapshot, error) {
	return client.request(ctx, http.MethodPost, "/v1/runtime/start")
}

func (client *Client) Stop(ctx context.Context) (Snapshot, error) {
	return client.request(ctx, http.MethodPost, "/v1/runtime/stop")
}

func (client *Client) request(ctx context.Context, method, path string) (Snapshot, error) {
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, bytes.NewReader(nil))
	if err != nil {
		return Snapshot{}, err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	response, err := client.http.Do(request)
	if err != nil {
		return Snapshot{}, err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxControlResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Snapshot{}, err
	}
	if len(body) > maxControlResponseBytes {
		return Snapshot{}, errors.New("Agent control response is too large")
	}
	if response.StatusCode == http.StatusOK {
		var snapshot Snapshot
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&snapshot); err != nil {
			return Snapshot{}, fmt.Errorf("decode Agent control status: %w", err)
		}
		return snapshot, nil
	}
	var failure struct {
		Code   string   `json:"code"`
		Status Snapshot `json:"status"`
	}
	if err := json.Unmarshal(body, &failure); err != nil || failure.Code == "" {
		failure.Code = "invalid_error_response"
	}
	return failure.Status, &APIError{Status: response.StatusCode, Code: failure.Code}
}
