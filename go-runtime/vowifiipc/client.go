package vowifiipc

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
	"time"
)

const maxResponseBytes = 1 << 20

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type ResponseError struct {
	Status  int
	Failure OperationError
}

func (failure *ResponseError) Error() string {
	return fmt.Sprintf("VoWiFi IPC request failed: HTTP %d: %s", failure.Status, failure.Failure.Error())
}

func NewClient(baseURL, token string, client *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.Port() == "" ||
		len(token) < minimumTokenBytes {
		return nil, errors.New("invalid VoWiFi IPC client configuration")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("VoWiFi IPC client requires a literal loopback endpoint")
	}
	if client == nil {
		client = &http.Client{}
	} else {
		clone := *client
		client = &clone
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), token: token, http: client}, nil
}

func (client *Client) Status(ctx context.Context) (Snapshot, error) {
	result, err := request[struct{}, Snapshot](ctx, client, http.MethodGet, "/v1/status", nil)
	if err == nil {
		err = result.Validate()
	}
	return result, err
}

func (client *Client) Start(ctx context.Context, input LifecycleRequest) (OperationResult, error) {
	if err := input.Validate(); err != nil {
		return OperationResult{}, err
	}
	result, err := request[LifecycleRequest, OperationResult](ctx, client, http.MethodPost, "/v1/runtime/start", &input)
	if err == nil {
		err = validateOperationResult(input.OperationID, result)
	}
	return result, err
}

func (client *Client) Stop(ctx context.Context, input LifecycleRequest) (OperationResult, error) {
	if err := input.Validate(); err != nil {
		return OperationResult{}, err
	}
	result, err := request[LifecycleRequest, OperationResult](ctx, client, http.MethodPost, "/v1/runtime/stop", &input)
	if err == nil {
		err = validateOperationResult(input.OperationID, result)
	}
	return result, err
}

func (client *Client) StartCall(ctx context.Context, input StartCallRequest) (CallResult, error) {
	if err := input.Validate(); err != nil {
		return CallResult{}, err
	}
	result, err := request[StartCallRequest, CallResult](ctx, client, http.MethodPost, "/v1/calls/start", &input)
	if err == nil {
		err = result.Validate()
		if err == nil && (result.OperationID != input.OperationID || result.CallID != input.CallID) {
			err = errors.New("VoWiFi IPC returned mismatched call result identity")
		}
	}
	return result, err
}

func (client *Client) EndCall(ctx context.Context, input EndCallRequest) (CallResult, error) {
	if err := input.Validate(); err != nil {
		return CallResult{}, err
	}
	result, err := request[EndCallRequest, CallResult](ctx, client, http.MethodPost, "/v1/calls/end", &input)
	if err == nil {
		err = result.Validate()
		if err == nil && (result.OperationID != input.OperationID || result.CallID != input.CallID) {
			err = errors.New("VoWiFi IPC returned mismatched call result identity")
		}
	}
	return result, err
}

func (client *Client) AnswerIncomingCall(ctx context.Context, input AnswerIncomingCallRequest) (CallResult, error) {
	if err := input.Validate(); err != nil {
		return CallResult{}, err
	}
	result, err := request[AnswerIncomingCallRequest, CallResult](ctx, client, http.MethodPost, "/v1/calls/incoming/answer", &input)
	if err == nil {
		err = validateIncomingCallResult(input.OperationID, input.CallID, result)
	}
	return result, err
}

func (client *Client) RejectIncomingCall(ctx context.Context, input RejectIncomingCallRequest) (CallResult, error) {
	if err := input.Validate(); err != nil {
		return CallResult{}, err
	}
	result, err := request[RejectIncomingCallRequest, CallResult](ctx, client, http.MethodPost, "/v1/calls/incoming/reject", &input)
	if err == nil {
		err = validateIncomingCallResult(input.OperationID, input.CallID, result)
	}
	return result, err
}

func (client *Client) SendMessage(ctx context.Context, input SendMessageRequest) (MessageResult, error) {
	if err := input.Validate(); err != nil {
		return MessageResult{}, err
	}
	result, err := request[SendMessageRequest, MessageResult](ctx, client, http.MethodPost, "/v1/messages/send", &input)
	if err == nil {
		err = result.Validate()
		if err == nil && (result.OperationID != input.OperationID || result.MessageID != input.MessageID) {
			err = errors.New("VoWiFi IPC returned mismatched message result identity")
		}
	}
	return result, err
}

func (client *Client) BeginDrain(ctx context.Context, input MaintenanceRequest) (MaintenanceResult, error) {
	return client.maintenance(ctx, "/v1/maintenance/drain", input, true)
}

func (client *Client) EndDrain(ctx context.Context, input MaintenanceRequest) (MaintenanceResult, error) {
	return client.maintenance(ctx, "/v1/maintenance/resume", input, false)
}

func (client *Client) maintenance(ctx context.Context, path string, input MaintenanceRequest, draining bool) (MaintenanceResult, error) {
	if err := input.Validate(); err != nil {
		return MaintenanceResult{}, err
	}
	result, err := request[MaintenanceRequest, MaintenanceResult](ctx, client, http.MethodPost, path, &input)
	if err == nil {
		err = result.Validate()
		if err == nil && (result.LeaseID != input.LeaseID || result.Draining != draining) {
			err = errors.New("VoWiFi IPC returned mismatched maintenance lease")
		}
	}
	return result, err
}

func request[Input any, Output any](
	ctx context.Context,
	client *Client,
	method, path string,
	input *Input,
) (Output, error) {
	var zero Output
	if client == nil {
		return zero, errors.New("nil VoWiFi IPC client")
	}
	var body io.Reader
	if input != nil {
		wire, err := json.Marshal(input)
		if err != nil {
			return zero, err
		}
		body = bytes.NewReader(wire)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return zero, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.token)
	if input != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(httpRequest)
	if err != nil {
		return zero, err
	}
	defer response.Body.Close()
	wire, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return zero, err
	}
	if len(wire) > maxResponseBytes {
		return zero, errors.New("VoWiFi IPC response is too large")
	}
	if response.StatusCode != http.StatusOK {
		var failure OperationError
		if err := decodeStrict(wire, &failure); err != nil || !validError(failure) {
			failure = OperationError{Kind: ErrorFailed, Code: "invalid_error_response"}
		}
		failure.RetryAfter = time.Duration(failure.RetryAfterMS) * time.Millisecond
		return zero, &ResponseError{Status: response.StatusCode, Failure: failure}
	}
	var output Output
	if err := decodeStrict(wire, &output); err != nil {
		return zero, fmt.Errorf("decode VoWiFi IPC response: %w", err)
	}
	return output, nil
}

func decodeStrict(wire []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing JSON")
	}
	return nil
}

func validError(failure OperationError) bool {
	switch failure.Kind {
	case ErrorInvalid, ErrorConflict, ErrorNotReady, ErrorNotFound, ErrorRejected, ErrorFailed:
	default:
		return false
	}
	return validIdentifier(failure.Code) && validCode(failure.Layer) && failure.RetryAfterMS >= 0
}
