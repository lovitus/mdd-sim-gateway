package vowifiipc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	minimumTokenBytes      = 32
	maxRequestBytes        = 64 << 10
	CapabilitiesHeader     = "X-MDD-VoWiFi-Capabilities"
	RecoveryStopCapability = "recovery-stop-v1"
)

type API struct {
	backend          Backend
	tokenHash        [sha256.Size]byte
	operationTimeout time.Duration
	mux              *http.ServeMux
}

func NewAPI(backend Backend, token string, operationTimeout time.Duration) (*API, error) {
	if backend == nil || len(token) < minimumTokenBytes || operationTimeout <= 0 {
		return nil, errors.New("invalid VoWiFi IPC API configuration")
	}
	api := &API{
		backend: backend, tokenHash: sha256.Sum256([]byte(token)),
		operationTimeout: operationTimeout, mux: http.NewServeMux(),
	}
	api.mux.HandleFunc("GET /healthz", api.health)
	api.mux.HandleFunc("GET /v1/status", api.authorized(api.status))
	api.mux.HandleFunc("POST /v1/runtime/start", api.authorized(api.start))
	api.mux.HandleFunc("POST /v1/runtime/stop", api.authorized(api.stop))
	api.mux.HandleFunc("POST /v1/register", api.authorized(api.register))
	api.mux.HandleFunc("POST /v1/calls/start", api.authorized(api.startCall))
	api.mux.HandleFunc("POST /v1/calls/end", api.authorized(api.endCall))
	api.mux.HandleFunc("POST /v1/calls/dtmf", api.authorized(api.sendDTMF))
	api.mux.HandleFunc("POST /v1/calls/incoming/answer", api.authorized(api.answerIncomingCall))
	api.mux.HandleFunc("POST /v1/calls/incoming/reject", api.authorized(api.rejectIncomingCall))
	api.mux.HandleFunc("POST /v1/messages/send", api.authorized(api.sendMessage))
	api.mux.HandleFunc("POST /v1/maintenance/drain", api.authorized(api.beginDrain))
	api.mux.HandleFunc("POST /v1/maintenance/resume", api.authorized(api.endDrain))
	return api, nil
}

func (api *API) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !loopbackRemote(request.RemoteAddr) {
		writeError(response, http.StatusForbidden, &OperationError{Kind: ErrorRejected, Code: "local_only"})
		return
	}
	api.mux.ServeHTTP(response, request)
}

func loopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (api *API) authorized(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		const prefix = "Bearer "
		header := request.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			writeError(response, http.StatusUnauthorized, &OperationError{Kind: ErrorRejected, Code: "unauthorized"})
			return
		}
		presented := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
		if subtle.ConstantTimeCompare(presented[:], api.tokenHash[:]) != 1 {
			writeError(response, http.StatusUnauthorized, &OperationError{Kind: ErrorRejected, Code: "unauthorized"})
			return
		}
		next(response, request)
	}
}

func (api *API) health(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set(CapabilitiesHeader, RecoveryStopCapability)
	writeJSON(response, http.StatusOK, map[string]any{
		"status": "ok", "component": "mdd-vowifi", "schema_version": SchemaVersion,
	})
}

func (api *API) status(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := api.context(request)
	defer cancel()
	snapshot, err := api.backend.Status(ctx)
	if err == nil {
		err = snapshot.Validate()
		if err != nil {
			err = fmt.Errorf("invalid provider snapshot: %w", err)
		}
	}
	writeResult(response, snapshot, err)
}

func (api *API) start(response http.ResponseWriter, request *http.Request) {
	var input LifecycleRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	result, err := api.backend.Start(ctx, input)
	if err == nil {
		err = validateOperationResult(input.OperationID, result)
	}
	writeResult(response, result, err)
}

func (api *API) stop(response http.ResponseWriter, request *http.Request) {
	var input LifecycleRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	result, err := api.backend.Stop(ctx, input)
	if err == nil {
		err = validateOperationResult(input.OperationID, result)
	}
	writeResult(response, result, err)
}

func (api *API) register(response http.ResponseWriter, request *http.Request) {
	var input RegisterRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	backend, ok := api.backend.(RegistrationBackend)
	if !ok {
		writeError(response, http.StatusConflict, &OperationError{Kind: ErrorRejected, Code: "register_unsupported", Layer: "ims"})
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	result, err := backend.Register(ctx, input)
	if err == nil {
		err = validateOperationResult(input.OperationID, result)
	}
	writeResult(response, result, err)
}

func (api *API) startCall(response http.ResponseWriter, request *http.Request) {
	var input StartCallRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	result, err := api.backend.StartCall(ctx, input)
	if err == nil {
		err = result.Validate()
		if err == nil && (result.OperationID != input.OperationID || result.CallID != input.CallID) {
			err = errors.New("provider returned mismatched call result identity")
		}
	}
	writeResult(response, result, err)
}

func (api *API) endCall(response http.ResponseWriter, request *http.Request) {
	var input EndCallRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	result, err := api.backend.EndCall(ctx, input)
	if err == nil {
		err = result.Validate()
		if err == nil && (result.OperationID != input.OperationID || result.CallID != input.CallID) {
			err = errors.New("provider returned mismatched call result identity")
		}
	}
	writeResult(response, result, err)
}

func (api *API) sendDTMF(response http.ResponseWriter, request *http.Request) {
	var input SendDTMFRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	backend, ok := api.backend.(DTMFBackend)
	if !ok {
		writeError(response, http.StatusConflict, &OperationError{Kind: ErrorRejected, Code: "dtmf_unsupported", Layer: "call"})
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	result, err := backend.SendDTMF(ctx, input)
	if err == nil {
		err = validateIncomingCallResult(input.OperationID, input.CallID, result)
	}
	writeResult(response, result, err)
}

func (api *API) answerIncomingCall(response http.ResponseWriter, request *http.Request) {
	var input AnswerIncomingCallRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	backend, ok := api.backend.(IncomingCallBackend)
	if !ok {
		writeError(response, http.StatusConflict, &OperationError{Kind: ErrorRejected, Code: "incoming_calls_unsupported", Layer: "voice"})
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	result, err := backend.AnswerIncomingCall(ctx, input)
	if err == nil {
		err = validateIncomingCallResult(input.OperationID, input.CallID, result)
	}
	writeResult(response, result, err)
}

func (api *API) rejectIncomingCall(response http.ResponseWriter, request *http.Request) {
	var input RejectIncomingCallRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	backend, ok := api.backend.(IncomingCallBackend)
	if !ok {
		writeError(response, http.StatusConflict, &OperationError{Kind: ErrorRejected, Code: "incoming_calls_unsupported", Layer: "voice"})
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	result, err := backend.RejectIncomingCall(ctx, input)
	if err == nil {
		err = validateIncomingCallResult(input.OperationID, input.CallID, result)
	}
	writeResult(response, result, err)
}

func validateIncomingCallResult(operationID, callID string, result CallResult) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if result.OperationID != operationID || result.CallID != callID {
		return errors.New("provider returned mismatched incoming call result identity")
	}
	return nil
}

func (api *API) sendMessage(response http.ResponseWriter, request *http.Request) {
	var input SendMessageRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	result, err := api.backend.SendMessage(ctx, input)
	if err == nil {
		err = result.Validate()
		if err == nil && (result.OperationID != input.OperationID || result.MessageID != input.MessageID) {
			err = errors.New("provider returned mismatched message result identity")
		}
	}
	writeResult(response, result, err)
}

func (api *API) beginDrain(response http.ResponseWriter, request *http.Request) {
	api.maintenance(response, request, true)
}

func (api *API) endDrain(response http.ResponseWriter, request *http.Request) {
	api.maintenance(response, request, false)
}

func (api *API) maintenance(response http.ResponseWriter, request *http.Request, begin bool) {
	var input MaintenanceRequest
	if !decodeRequest(response, request, &input) || !validateRequest(response, input.Validate()) {
		return
	}
	backend, ok := api.backend.(MaintenanceBackend)
	if !ok {
		writeError(response, http.StatusConflict, &OperationError{
			Kind: ErrorRejected, Code: "maintenance_unsupported", Layer: "maintenance",
		})
		return
	}
	ctx, cancel := api.context(request)
	defer cancel()
	var result MaintenanceResult
	var err error
	if begin {
		result, err = backend.BeginDrain(ctx, input)
	} else {
		result, err = backend.EndDrain(ctx, input)
	}
	if err == nil {
		err = result.Validate()
		if err == nil && (result.LeaseID != input.LeaseID || result.Draining != begin) {
			err = errors.New("provider returned mismatched maintenance lease")
		}
	}
	writeResult(response, result, err)
}

func (api *API) context(request *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(request.Context(), api.operationTimeout)
}

func validateOperationResult(operationID string, result OperationResult) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if result.OperationID != operationID {
		return errors.New("provider returned mismatched operation identity")
	}
	return nil
}

func validateRequest(response http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	writeError(response, http.StatusBadRequest, &OperationError{
		Kind: ErrorInvalid, Code: "invalid_request", Detail: err.Error(),
	})
	return false
}

func decodeRequest(response http.ResponseWriter, request *http.Request, value any) bool {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType, &OperationError{
			Kind: ErrorInvalid, Code: "json_content_type_required",
		})
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(response, http.StatusRequestEntityTooLarge, &OperationError{Kind: ErrorInvalid, Code: "request_too_large"})
			return false
		}
		writeError(response, http.StatusBadRequest, &OperationError{Kind: ErrorInvalid, Code: "invalid_json"})
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(response, http.StatusRequestEntityTooLarge, &OperationError{Kind: ErrorInvalid, Code: "request_too_large"})
			return false
		}
		writeError(response, http.StatusBadRequest, &OperationError{Kind: ErrorInvalid, Code: "invalid_json"})
		return false
	}
	return true
}

func writeResult(response http.ResponseWriter, value any, err error) {
	if err == nil {
		writeJSON(response, http.StatusOK, value)
		return
	}
	var operationError *OperationError
	if errors.As(err, &operationError) {
		failure := *operationError
		if failure.RetryAfter > 0 {
			failure.RetryAfterMS = failure.RetryAfter.Milliseconds()
		}
		writeError(response, statusForError(failure.Kind), &failure)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		writeError(response, http.StatusGatewayTimeout, &OperationError{Kind: ErrorFailed, Code: "operation_timeout"})
		return
	}
	writeError(response, http.StatusInternalServerError, &OperationError{Kind: ErrorFailed, Code: "operation_failed"})
}

func statusForError(kind ErrorKind) int {
	switch kind {
	case ErrorInvalid:
		return http.StatusBadRequest
	case ErrorConflict:
		return http.StatusConflict
	case ErrorNotReady:
		return http.StatusPreconditionFailed
	case ErrorNotFound:
		return http.StatusNotFound
	case ErrorRejected:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

func writeError(response http.ResponseWriter, status int, failure *OperationError) {
	writeJSON(response, status, failure)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
