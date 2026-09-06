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
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const (
	maximumRequestBytes      = 64 << 10
	maximumOperationDuration = 125 * time.Second
)

type Handler struct {
	providers  *mediaauth.ProviderDirectory
	http       *http.Client
	catalog    PaidActionCatalog
	cardRoutes CardRouteResolver
	allowance  AllowanceDispatchAuthorizer
	calls      CallRecorder
	requestMu  sync.RWMutex
	requester  RuntimeIntentRequester
}

type CallRecorder interface {
	Start(lineID, transport, callID, direction, peer string, at time.Time) error
	Active(lineID, transport, callID string, at time.Time) error
	Finish(lineID, transport, callID, status string, at time.Time) error
}

// PaidActionCatalog resolves the current durable SIM identity immediately
// before an outgoing carrier action is dispatched.
type PaidActionCatalog interface {
	Get(string) (linecatalog.Line, error)
}

type CardRouteResolver interface {
	ResolveCardRoute(string) (agentlink.CardRouteTarget, error)
}

type AllowanceDispatchAuthorizer interface {
	AuthorizeDispatch(queryID, transport, lineID, expectedCardID, operationID, messageID, recipient, body string) error
}

// RuntimeIntentRequester is the narrow synchronous facade implemented by the
// runtime reconciler. Public lifecycle requests persist intent through it and
// wait for an exact observed target; they never invoke a Provider directly.
type RuntimeIntentRequester interface {
	RequestIntent(context.Context, string, bool, string) (vowifiipc.OperationResult, error)
}

type Option func(*Handler) error

func WithCallRecorder(recorder CallRecorder) Option {
	return func(handler *Handler) error {
		if recorder == nil {
			return errors.New("call recorder is required")
		}
		handler.calls = recorder
		return nil
	}
}

func WithCardRouteResolver(resolver CardRouteResolver) Option {
	return func(handler *Handler) error {
		if resolver == nil {
			return errors.New("card route resolver is required")
		}
		handler.cardRoutes = resolver
		return nil
	}
}

func (handler *Handler) BindAllowanceDispatchAuthorizer(authorizer AllowanceDispatchAuthorizer) error {
	if handler == nil || authorizer == nil {
		return errors.New("allowance dispatch authorizer is required")
	}
	handler.requestMu.Lock()
	defer handler.requestMu.Unlock()
	if handler.allowance != nil {
		return errors.New("allowance dispatch authorizer is already bound")
	}
	handler.allowance = authorizer
	return nil
}

// BindRuntimeIntentRequester resolves the construction cycle between the
// Provider client and the reconciler. It is deliberately one-shot and must be
// called before the public server starts accepting lifecycle requests.
func (handler *Handler) BindRuntimeIntentRequester(requester RuntimeIntentRequester) error {
	if handler == nil || requester == nil {
		return errors.New("runtime intent requester is required")
	}
	handler.requestMu.Lock()
	defer handler.requestMu.Unlock()
	if handler.requester != nil {
		return errors.New("runtime intent requester is already bound")
	}
	handler.requester = requester
	return nil
}

func (handler *Handler) runtimeIntentRequester() RuntimeIntentRequester {
	handler.requestMu.RLock()
	defer handler.requestMu.RUnlock()
	return handler.requester
}

func (handler *Handler) allowanceDispatchAuthorizer() AllowanceDispatchAuthorizer {
	handler.requestMu.RLock()
	defer handler.requestMu.RUnlock()
	return handler.allowance
}

// Observe returns one current provider-owned snapshot together with the exact
// route fence used for that read. It performs the same generation and line
// identity validation as ServeHTTP.
func (handler *Handler) Observe(ctx context.Context, lineID string) (vowifiipc.Snapshot, mediaauth.ProviderFence, error) {
	var result vowifiipc.Snapshot
	var fence mediaauth.ProviderFence
	err := handler.providers.UseCurrent(ctx, strings.TrimSpace(lineID), func(provider mediaauth.Provider) error {
		fence = provider.Fence()
		client, err := handler.client(provider)
		if err != nil {
			return err
		}
		result, err = client.Status(ctx)
		if err != nil {
			return err
		}
		return validateIdentity(result, lineID, provider.ProviderID, provider.Generation)
	})
	return result, fence, err
}

func (handler *Handler) Status(ctx context.Context, lineID string) (vowifiipc.Snapshot, error) {
	lineID = strings.TrimSpace(lineID)
	status, fence, err := handler.Observe(ctx, lineID)
	if err != nil {
		return status, err
	}
	line, err := handler.catalog.Get(lineID)
	if err != nil {
		if errors.Is(err, linecatalog.ErrNotFound) {
			return status, &vowifiipc.OperationError{
				Kind: vowifiipc.ErrorNotFound, Code: "line_not_found", Layer: "card_route",
			}
		}
		return status, &vowifiipc.OperationError{
			Kind: vowifiipc.ErrorFailed, Code: "provider_card_binding_unavailable", Layer: "card_route",
		}
	}
	if fence.CardID != line.CardID {
		return status, &vowifiipc.OperationError{
			Kind: vowifiipc.ErrorConflict, Code: "provider_card_mismatch", Layer: "card_route",
			Detail: "the current Provider is bound to a different SIM identity",
		}
	}
	return status, nil
}

// VerifyMessageRoute proves the current Provider generation and the live
// AKA-capable Agent attachment describe the same selected ICCID. It performs
// no paid action and grants no reusable capability.
func (handler *Handler) VerifyMessageRoute(ctx context.Context, lineID, expectedCardID string) error {
	lineID, expectedCardID = strings.TrimSpace(lineID), strings.TrimSpace(expectedCardID)
	line, err := handler.catalog.Get(lineID)
	if err != nil || !line.Enabled || line.CardID != expectedCardID || handler.cardRoutes == nil {
		return &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "card_route_unavailable", Layer: "card_route"}
	}
	return handler.providers.UseCurrent(ctx, lineID, func(provider mediaauth.Provider) error {
		if provider.CardID != expectedCardID {
			return errPaidCardMismatch
		}
		if _, routeErr := handler.cardRoutes.ResolveCardRoute(expectedCardID); routeErr != nil {
			return &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "card_route_unavailable", Layer: "card_route"}
		}
		client, clientErr := handler.client(provider)
		if clientErr != nil {
			return clientErr
		}
		status, statusErr := client.Status(ctx)
		if statusErr != nil {
			return statusErr
		}
		if validateIdentity(status, lineID, provider.ProviderID, provider.Generation) != nil {
			return errInvalidResponse
		}
		if !status.Messaging.Available || status.Maintenance.Draining {
			return &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "messaging_transport_unavailable", Layer: "messaging"}
		}
		return nil
	})
}

func (handler *Handler) Start(ctx context.Context, lineID string, fence mediaauth.ProviderFence, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	return handler.lifecycle(ctx, lineID, fence, request, true)
}

func (handler *Handler) Stop(ctx context.Context, lineID string, fence mediaauth.ProviderFence, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	return handler.lifecycle(ctx, lineID, fence, request, false)
}

func (handler *Handler) lifecycle(ctx context.Context, lineID string, fence mediaauth.ProviderFence, request vowifiipc.LifecycleRequest, start bool) (vowifiipc.OperationResult, error) {
	var result vowifiipc.OperationResult
	lineID = strings.TrimSpace(lineID)
	if request.Validate() != nil || fence.LineID != lineID {
		return result, errInvalidRequest
	}
	err := handler.providers.UseExpected(ctx, fence, func(provider mediaauth.Provider) error {
		client, err := handler.client(provider)
		if err != nil {
			return err
		}
		if !start && request.RequireIdle {
			supported, probeErr := client.SupportsRecoveryStop(ctx)
			if probeErr != nil {
				return &vowifiipc.OperationError{
					Kind: vowifiipc.ErrorNotReady, Code: "recovery_stop_capability_unavailable", Layer: "runtime",
				}
			}
			if !supported {
				return &vowifiipc.OperationError{
					Kind: vowifiipc.ErrorNotReady, Code: "recovery_stop_unsupported", Layer: "runtime",
				}
			}
		}
		if start {
			result, err = client.Start(ctx, request)
		} else {
			result, err = client.Stop(ctx, request)
		}
		if err != nil {
			return err
		}
		return validateIdentity(result, lineID, provider.ProviderID, provider.Generation)
	})
	return result, err
}

func NewHandler(providers *mediaauth.ProviderDirectory, catalog PaidActionCatalog, client *http.Client, options ...Option) (*Handler, error) {
	if providers == nil || catalog == nil {
		return nil, errors.New("provider control directory and paid-action catalog are required")
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
	handler := &Handler{providers: providers, catalog: catalog, http: client}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("nil provider control option")
		}
		if err := option(handler); err != nil {
			return nil, err
		}
	}
	return handler, nil
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
	prepared, err := prepareOperation(request, operation)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	if prepared.expectedCardID != "" {
		line, catalogErr := handler.catalog.Get(lineID)
		if catalogErr != nil {
			if errors.Is(catalogErr, linecatalog.ErrNotFound) {
				writeFailure(response, http.StatusNotFound, vowifiipc.OperationError{
					Kind: vowifiipc.ErrorNotFound, Code: "line_not_found", Layer: "card_route",
				})
				return
			}
			writeFailure(response, http.StatusInternalServerError, vowifiipc.OperationError{
				Kind: vowifiipc.ErrorFailed, Code: "paid_action_identity_unavailable", Layer: "card_route",
			})
			return
		}
		if line.CardID != prepared.expectedCardID {
			writeFailure(response, http.StatusConflict, vowifiipc.OperationError{
				Kind: vowifiipc.ErrorConflict, Code: "paid_action_card_mismatch", Layer: "card_route",
				Detail: "the selected SIM identity changed before the carrier action",
			})
			return
		}
		if !line.Enabled {
			writeFailure(response, http.StatusPreconditionFailed, vowifiipc.OperationError{
				Kind: vowifiipc.ErrorNotReady, Code: "line_disabled", Layer: "intent",
			})
			return
		}
	}
	if prepared.allowanceQueryID != "" {
		authorizer := handler.allowanceDispatchAuthorizer()
		if authorizer == nil || prepared.messageRequest == nil {
			writeFailure(response, http.StatusPreconditionFailed, vowifiipc.OperationError{
				Kind: vowifiipc.ErrorNotReady, Code: "allowance_query_unavailable", Layer: "messaging",
			})
			return
		}
		message := prepared.messageRequest
		if err := authorizer.AuthorizeDispatch(prepared.allowanceQueryID, "vowifi", lineID, prepared.expectedCardID,
			message.OperationID, message.MessageID, message.Recipient, message.Body); err != nil {
			writeFailure(response, http.StatusConflict, vowifiipc.OperationError{
				Kind: vowifiipc.ErrorConflict, Code: "allowance_query_changed", Layer: "messaging",
			})
			return
		}
	}
	operationContext, cancel := context.WithTimeout(request.Context(), maximumOperationDuration)
	defer cancel()
	if prepared.status {
		result, statusErr := handler.Status(operationContext, lineID)
		if statusErr != nil {
			handler.writeError(response, statusErr)
			return
		}
		writeJSON(response, http.StatusOK, result)
		return
	}
	if prepared.runtime != nil {
		requester := handler.runtimeIntentRequester()
		if requester == nil {
			handler.writeError(response, errRuntimeCoordinatorUnavailable)
			return
		}
		result, requestErr := requester.RequestIntent(operationContext, lineID,
			prepared.runtime.enabled, prepared.runtime.operationID)
		if requestErr != nil {
			handler.writeError(response, requestErr)
			return
		}
		writeJSON(response, http.StatusOK, result)
		return
	}
	if prepared.call != nil && prepared.call.action == "start" && handler.calls != nil {
		_ = handler.calls.Start(lineID, "vowifi", prepared.call.callID,
			prepared.call.direction, prepared.call.peer, time.Now().UTC())
	}

	var result any
	err = handler.providers.UseCurrent(operationContext, lineID, func(provider mediaauth.Provider) error {
		if prepared.expectedCardID != "" && provider.CardID != prepared.expectedCardID {
			return errPaidCardMismatch
		}
		if prepared.message {
			if handler.cardRoutes == nil {
				return &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "card_route_unavailable", Layer: "card_route"}
			}
			if _, routeErr := handler.cardRoutes.ResolveCardRoute(prepared.expectedCardID); routeErr != nil {
				return &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "card_route_unavailable", Layer: "card_route"}
			}
		}
		client, err := handler.client(provider)
		if err != nil {
			return err
		}
		result, err = prepared.invoke(operationContext, client)
		if err != nil {
			return err
		}
		return validateIdentity(result, lineID, provider.ProviderID, provider.Generation)
	})
	if err != nil {
		if prepared.call != nil && prepared.call.action == "start" && handler.calls != nil {
			_ = handler.calls.Finish(lineID, "vowifi", prepared.call.callID, "failed", time.Now().UTC())
		}
		handler.writeError(response, err)
		return
	}
	if prepared.call != nil && handler.calls != nil {
		now := time.Now().UTC()
		switch prepared.call.action {
		case "start":
			_ = handler.calls.Active(lineID, "vowifi", prepared.call.callID, now)
		case "end":
			_ = handler.calls.Finish(lineID, "vowifi", prepared.call.callID, "ended", now)
		case "reject":
			_ = handler.calls.Start(lineID, "vowifi", prepared.call.callID, "in", prepared.call.peer, now)
			_ = handler.calls.Finish(lineID, "vowifi", prepared.call.callID, "rejected", now)
		}
	}
	writeJSON(response, http.StatusOK, result)
}

type invocation func(context.Context, *vowifiipc.Client) (any, error)

type preparedOperation struct {
	invoke           invocation
	expectedCardID   string
	message          bool
	allowanceQueryID string
	messageRequest   *vowifiipc.SendMessageRequest
	call             *callMutation
	runtime          *runtimeMutation
	status           bool
}

type callMutation struct {
	action, callID, direction, peer string
}

type runtimeMutation struct {
	operationID string
	enabled     bool
}

func prepareOperation(request *http.Request, operation string) (preparedOperation, error) {
	if request.URL.RawQuery != "" {
		return preparedOperation{}, errInvalidRequest
	}
	switch operation {
	case "status":
		if request.Method != http.MethodGet {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{status: true}, nil
	case "runtime/start":
		if request.Method != http.MethodPost {
			return preparedOperation{}, errInvalidRequest
		}
		var input vowifiipc.LifecycleRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil || input.RequireIdle {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{runtime: &runtimeMutation{operationID: input.OperationID, enabled: true}}, nil
	case "runtime/stop":
		if request.Method != http.MethodPost {
			return preparedOperation{}, errInvalidRequest
		}
		var input vowifiipc.LifecycleRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil || input.RequireIdle {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{runtime: &runtimeMutation{operationID: input.OperationID}}, nil
	case "register":
		if request.Method != http.MethodPost {
			return preparedOperation{}, errInvalidRequest
		}
		var input struct {
			OperationID    string `json:"operation_id"`
			ExpectedCardID string `json:"expected_card_id"`
		}
		if err := decodeRequest(request, &input); err != nil || !validCardID(input.ExpectedCardID) {
			return preparedOperation{}, errInvalidRequest
		}
		providerRequest := vowifiipc.RegisterRequest{OperationID: input.OperationID}
		if providerRequest.Validate() != nil {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{expectedCardID: strings.TrimSpace(input.ExpectedCardID),
			invoke: func(ctx context.Context, client *vowifiipc.Client) (any, error) {
				return client.Register(ctx, providerRequest)
			}}, nil
	case "calls/start":
		if request.Method != http.MethodPost {
			return preparedOperation{}, errInvalidRequest
		}
		var input struct {
			OperationID      string `json:"operation_id"`
			CallID           string `json:"call_id"`
			MediaSessionID   string `json:"media_session_id,omitempty"`
			Callee           string `json:"callee"`
			MediaBufferMS    int    `json:"media_buffer_ms"`
			ExpectedCardID   string `json:"expected_card_id"`
			AllowanceQueryID string `json:"allowance_query_id,omitempty"`
		}
		if err := decodeRequest(request, &input); err != nil || !validCardID(input.ExpectedCardID) {
			return preparedOperation{}, errInvalidRequest
		}
		providerRequest := vowifiipc.StartCallRequest{
			OperationID: input.OperationID, CallID: input.CallID, MediaSessionID: input.MediaSessionID,
			Callee: input.Callee, MediaBufferMS: input.MediaBufferMS,
		}
		if providerRequest.Validate() != nil {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{
			expectedCardID: strings.TrimSpace(input.ExpectedCardID),
			call:           &callMutation{action: "start", callID: input.CallID, direction: "out", peer: input.Callee},
			invoke: func(ctx context.Context, client *vowifiipc.Client) (any, error) {
				return client.StartCall(ctx, providerRequest)
			},
		}, nil
	case "calls/end":
		if request.Method != http.MethodPost {
			return preparedOperation{}, errInvalidRequest
		}
		var input vowifiipc.EndCallRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{call: &callMutation{action: "end", callID: input.CallID}, invoke: func(ctx context.Context, client *vowifiipc.Client) (any, error) { return client.EndCall(ctx, input) }}, nil
	case "calls/dtmf":
		if request.Method != http.MethodPost {
			return preparedOperation{}, errInvalidRequest
		}
		var input vowifiipc.SendDTMFRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{invoke: func(ctx context.Context, client *vowifiipc.Client) (any, error) {
			return client.SendDTMF(ctx, input)
		}}, nil
	case "calls/incoming/answer":
		if request.Method != http.MethodPost {
			return preparedOperation{}, errInvalidRequest
		}
		var input vowifiipc.AnswerIncomingCallRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{call: &callMutation{action: "start", callID: input.CallID, direction: "in"}, invoke: func(ctx context.Context, client *vowifiipc.Client) (any, error) {
			return client.AnswerIncomingCall(ctx, input)
		}}, nil
	case "calls/incoming/reject":
		if request.Method != http.MethodPost {
			return preparedOperation{}, errInvalidRequest
		}
		var input vowifiipc.RejectIncomingCallRequest
		if err := decodeRequest(request, &input); err != nil || input.Validate() != nil {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{call: &callMutation{action: "reject", callID: input.CallID, direction: "in"}, invoke: func(ctx context.Context, client *vowifiipc.Client) (any, error) {
			return client.RejectIncomingCall(ctx, input)
		}}, nil
	case "messages/send":
		if request.Method != http.MethodPost {
			return preparedOperation{}, errInvalidRequest
		}
		var input struct {
			OperationID      string `json:"operation_id"`
			MessageID        string `json:"message_id"`
			Recipient        string `json:"recipient"`
			Body             string `json:"body"`
			ExpectedCardID   string `json:"expected_card_id"`
			AllowanceQueryID string `json:"allowance_query_id,omitempty"`
		}
		if err := decodeRequest(request, &input); err != nil {
			return preparedOperation{}, errInvalidRequest
		}
		providerRequest := vowifiipc.SendMessageRequest{
			OperationID: input.OperationID, MessageID: input.MessageID, Recipient: input.Recipient, Body: input.Body,
		}
		if providerRequest.Validate() != nil || !validCardID(input.ExpectedCardID) {
			return preparedOperation{}, errInvalidRequest
		}
		return preparedOperation{expectedCardID: strings.TrimSpace(input.ExpectedCardID), message: true,
			allowanceQueryID: strings.TrimSpace(input.AllowanceQueryID), messageRequest: &providerRequest,
			invoke: func(ctx context.Context, client *vowifiipc.Client) (any, error) {
				return client.SendMessage(ctx, providerRequest)
			}}, nil
	default:
		return preparedOperation{}, errUnknownOperation
	}
}

func validCardID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func knownOperation(operation string) bool {
	switch operation {
	case "status", "runtime/start", "runtime/stop", "register", "calls/start", "calls/end", "calls/dtmf", "calls/incoming/answer", "calls/incoming/reject", "messages/send":
		return true
	default:
		return false
	}
}

var (
	errInvalidRequest                = errors.New("invalid public provider request")
	errUnknownOperation              = errors.New("unknown provider operation")
	errInvalidResponse               = errors.New("provider returned mismatched runtime identity")
	errPaidCardMismatch              = errors.New("selected SIM identity does not match the current provider")
	errRuntimeCoordinatorUnavailable = errors.New("runtime intent coordinator is unavailable")
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
	var operationFailure *vowifiipc.OperationError
	switch {
	case errors.Is(err, errUnknownOperation):
		writeFailure(response, http.StatusNotFound, vowifiipc.OperationError{Kind: vowifiipc.ErrorNotFound, Code: "operation_not_found"})
	case errors.Is(err, errInvalidRequest):
		writeFailure(response, http.StatusBadRequest, vowifiipc.OperationError{Kind: vowifiipc.ErrorInvalid, Code: "invalid_request"})
	case errors.Is(err, errPaidCardMismatch):
		writeFailure(response, http.StatusConflict, vowifiipc.OperationError{
			Kind: vowifiipc.ErrorConflict, Code: "paid_action_card_mismatch", Layer: "card_route",
			Detail: "the selected SIM identity does not match the current Provider binding",
		})
	case errors.Is(err, mediaauth.ErrProviderUnavailable):
		writeFailure(response, http.StatusPreconditionFailed, vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "provider_unavailable", Layer: "runtime"})
	case errors.Is(err, mediaauth.ErrProviderFenceConflict):
		writeFailure(response, http.StatusConflict, vowifiipc.OperationError{Kind: vowifiipc.ErrorConflict, Code: "provider_route_changed", Layer: "runtime"})
	case errors.Is(err, errRuntimeCoordinatorUnavailable):
		writeFailure(response, http.StatusServiceUnavailable, vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "runtime_coordinator_unavailable", Layer: "intent"})
	case errors.As(err, &providerFailure):
		writeFailure(response, providerFailure.Status, providerFailure.Failure)
	case errors.As(err, &operationFailure):
		writeFailure(response, statusForOperationFailure(operationFailure.Kind), *operationFailure)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeFailure(response, http.StatusGatewayTimeout, vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "operation_timeout"})
	case errors.Is(err, errInvalidResponse):
		writeFailure(response, http.StatusBadGateway, vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "invalid_provider_response"})
	default:
		writeFailure(response, http.StatusBadGateway, vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "provider_transport_failed"})
	}
}

func statusForOperationFailure(kind vowifiipc.ErrorKind) int {
	switch kind {
	case vowifiipc.ErrorInvalid:
		return http.StatusBadRequest
	case vowifiipc.ErrorConflict, vowifiipc.ErrorRejected:
		return http.StatusConflict
	case vowifiipc.ErrorNotReady:
		return http.StatusPreconditionFailed
	case vowifiipc.ErrorNotFound:
		return http.StatusNotFound
	default:
		return http.StatusBadGateway
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
