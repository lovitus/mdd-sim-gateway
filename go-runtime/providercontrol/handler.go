// Package providercontrol routes authenticated public line operations to the
// current same-host VoWiFi provider without exposing its loopback token.
package providercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const (
	maximumRequestBytes      = 64 << 10
	maximumOperationDuration = 125 * time.Second
)

type Handler struct {
	providers *mediaauth.ProviderDirectory
	http      *http.Client
}

func NewHandler(providers *mediaauth.ProviderDirectory, client *http.Client) (*Handler, error) {
	if providers == nil {
		return nil, errors.New("provider control directory is required")
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		}}
	} else {
		clone := *client
		client = &clone
	}
	return &Handler{providers: providers, http: client}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	operation := strings.Trim(request.PathValue("operation"), "/")
	if lineID == "" || operation == "" {
		writeFailure(response, http.StatusBadRequest, vowifiipc.OperationError{Kind: vowifiipc.ErrorInvalid, Code: "invalid_route"})
		return
	}
	if !knownOperation(operation) {
		handler.writeError(response, errUnknownOperation)
		return
	}
	invoke, err := prepareOperation(request, operation)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	operationContext, cancel := context.WithTimeout(request.Context(), maximumOperationDuration)
	defer cancel()

	var result any
	err = handler.providers.UseCurrent(operationContext, lineID, func(provider mediaauth.Provider) error {
		client, err := handler.client(provider)
		if err != nil {
			return err
		}
		result, err = invoke(operationContext, client)
		if err != nil {
			return err
		}
		return validateIdentity(result, lineID, provider.ProviderID, provider.Generation)
	})
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

type invocation func(context.Context, *vowifiipc.Client) (any, error)

func prepareOperation(request *http.Request, operation string) (invocation, error) {
	if request.URL.RawQuery != "" {
		return nil, errInvalidRequest
	}
	switch operation {
	case "status":
		if request.Method != http.MethodGet {
			return nil, errInvalidRequest
		}
		return func(ctx context.Context, client *vowifiipc.Client) (any, error) { return client.Status(ctx) }, nil
	case "runtime/start":
		if request.Method != http.MethodPost {
			return nil, errInvalidRequest
		}
		var input vowifiipc.LifecycleRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return nil, errInvalidRequest
		}
		return func(ctx context.Context, client *vowifiipc.Client) (any, error) { return client.Start(ctx, input) }, nil
	case "runtime/stop":
		if request.Method != http.MethodPost {
			return nil, errInvalidRequest
		}
		var input vowifiipc.LifecycleRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return nil, errInvalidRequest
		}
		return func(ctx context.Context, client *vowifiipc.Client) (any, error) { return client.Stop(ctx, input) }, nil
	case "calls/start":
		if request.Method != http.MethodPost {
			return nil, errInvalidRequest
		}
		var input vowifiipc.StartCallRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return nil, errInvalidRequest
		}
		return func(ctx context.Context, client *vowifiipc.Client) (any, error) { return client.StartCall(ctx, input) }, nil
	case "calls/end":
		if request.Method != http.MethodPost {
			return nil, errInvalidRequest
		}
		var input vowifiipc.EndCallRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return nil, errInvalidRequest
		}
		return func(ctx context.Context, client *vowifiipc.Client) (any, error) { return client.EndCall(ctx, input) }, nil
	case "calls/incoming/answer":
		if request.Method != http.MethodPost {
			return nil, errInvalidRequest
		}
		var input vowifiipc.AnswerIncomingCallRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return nil, errInvalidRequest
		}
		return func(ctx context.Context, client *vowifiipc.Client) (any, error) {
			return client.AnswerIncomingCall(ctx, input)
		}, nil
	case "calls/incoming/reject":
		if request.Method != http.MethodPost {
			return nil, errInvalidRequest
		}
		var input vowifiipc.RejectIncomingCallRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return nil, errInvalidRequest
		}
		return func(ctx context.Context, client *vowifiipc.Client) (any, error) {
			return client.RejectIncomingCall(ctx, input)
		}, nil
	case "messages/send":
		if request.Method != http.MethodPost {
			return nil, errInvalidRequest
		}
		var input vowifiipc.SendMessageRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return nil, errInvalidRequest
		}
		return func(ctx context.Context, client *vowifiipc.Client) (any, error) {
			return client.SendMessage(ctx, input)
		}, nil
	default:
		return nil, errUnknownOperation
	}
}

func knownOperation(operation string) bool {
	switch operation {
	case "status", "runtime/start", "runtime/stop", "calls/start", "calls/end", "calls/incoming/answer", "calls/incoming/reject", "messages/send":
		return true
	default:
		return false
	}
}

var (
	errInvalidRequest   = errors.New("invalid public provider request")
	errUnknownOperation = errors.New("unknown provider operation")
	errInvalidResponse  = errors.New("provider returned mismatched runtime identity")
)

func (handler *Handler) client(provider mediaauth.Provider) (*vowifiipc.Client, error) {
	parsed, err := url.Parse(provider.BaseURL)
	if err != nil || parsed.Scheme != "ws" {
		return nil, errInvalidResponse
	}
	parsed.Scheme = "http"
	return vowifiipc.NewClient(parsed.String(), provider.Token, handler.http)
}

func validateIdentity(result any, lineID, providerID, generation string) error {
	var status vowifiipc.Snapshot
	switch value := result.(type) {
	case vowifiipc.Snapshot:
		status = value
	case vowifiipc.OperationResult:
		status = value.Status
	case vowifiipc.CallResult:
		status = value.Status
	case vowifiipc.MessageResult:
		status = value.Status
	default:
		return errInvalidResponse
	}
	if status.LineID != lineID || status.ProviderID != providerID || status.ProcessGeneration != generation {
		return errInvalidResponse
	}
	return nil
}

func decodeRequest(request *http.Request, target any) error {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return errInvalidRequest
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumRequestBytes {
		return errInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errInvalidRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errInvalidRequest
	}
	return nil
}

func (handler *Handler) writeError(response http.ResponseWriter, err error) {
	var providerFailure *vowifiipc.ResponseError
	switch {
	case errors.Is(err, errUnknownOperation):
		writeFailure(response, http.StatusNotFound, vowifiipc.OperationError{Kind: vowifiipc.ErrorNotFound, Code: "operation_not_found"})
	case errors.Is(err, errInvalidRequest):
		writeFailure(response, http.StatusBadRequest, vowifiipc.OperationError{Kind: vowifiipc.ErrorInvalid, Code: "invalid_request"})
	case errors.Is(err, mediaauth.ErrProviderUnavailable):
		writeFailure(response, http.StatusPreconditionFailed, vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "provider_unavailable", Layer: "runtime"})
	case errors.As(err, &providerFailure):
		writeFailure(response, providerFailure.Status, providerFailure.Failure)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeFailure(response, http.StatusGatewayTimeout, vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "operation_timeout"})
	case errors.Is(err, errInvalidResponse):
		writeFailure(response, http.StatusBadGateway, vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "invalid_provider_response"})
	default:
		writeFailure(response, http.StatusBadGateway, vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "provider_transport_failed"})
	}
}

func writeFailure(response http.ResponseWriter, status int, failure vowifiipc.OperationError) {
	writeJSON(response, status, failure)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
