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

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
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
	var snapshot Snapshot
	failure, err := client.request(ctx, http.MethodGet, "/v1/status", &snapshot)
	if err != nil {
		return failure, err
	}
	return snapshot, err
}

func (client *Client) Start(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	failure, err := client.request(ctx, http.MethodPost, "/v1/runtime/start", &snapshot)
	if err != nil {
		return failure, err
	}
	return snapshot, err
}

func (client *Client) Stop(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	failure, err := client.request(ctx, http.MethodPost, "/v1/runtime/stop", &snapshot)
	if err != nil {
		return failure, err
	}
	return snapshot, err
}

func (client *Client) Topology(ctx context.Context) (agentlink.TopologySnapshot, error) {
	var topology agentlink.TopologySnapshot
	_, err := client.request(ctx, http.MethodGet, "/v1/topology", &topology)
	if err == nil {
		err = topology.Validate()
	}
	return topology, err
}

func (client *Client) request(ctx context.Context, method, path string, result any) (Snapshot, error) {
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
		if err := decodeControlJSON(body, result); err != nil {
			return Snapshot{}, err
		}
		return Snapshot{}, nil
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

func decodeControlJSON(body []byte, result any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("decode Agent control response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Agent control response has trailing JSON")
	}
	return nil
}
