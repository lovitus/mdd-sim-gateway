package agentlink

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
	"strings"
	"time"
)

const maximumBrokerRequest = 16 << 10

type Broker interface {
	AuthenticateCardAKA(context.Context, AKAChallenge) (AKAResponse, error)
}

type BrokerRequest struct {
	AKA AKAChallenge `json:"aka"`
}

func (request BrokerRequest) Validate() error {
	return request.AKA.Validate()
}

type BrokerAPI struct {
	broker    Broker
	tokenHash [sha256.Size]byte
	timeout   time.Duration
}

func NewBrokerAPI(broker Broker, token string, timeout time.Duration) (*BrokerAPI, error) {
	if broker == nil || len(token) < minimumTokenBytes || timeout <= 0 || timeout > maximumOperationTimeout {
		return nil, errors.New("invalid Agent AKA broker API configuration")
	}
	return &BrokerAPI{broker: broker, tokenHash: sha256.Sum256([]byte(token)), timeout: timeout}, nil
}

func (api *BrokerAPI) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/v1/agent/aka" {
		http.NotFound(response, request)
		return
	}
	if !literalLoopbackRemote(request.RemoteAddr) {
		writeBrokerError(response, http.StatusForbidden, RemoteError{Kind: "rejected", Code: "loopback_required"})
		return
	}
	if !api.authorized(request.Header.Get("Authorization")) {
		writeBrokerError(response, http.StatusUnauthorized, RemoteError{Kind: "rejected", Code: "unauthorized"})
		return
	}
	var input BrokerRequest
	if err := decodeBrokerJSON(request.Body, &input); err != nil || input.Validate() != nil {
		writeBrokerError(response, http.StatusBadRequest, RemoteError{Kind: "rejected", Code: "invalid_request"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), api.timeout)
	result, err := api.broker.AuthenticateCardAKA(ctx, input.AKA)
	cancel()
	if result.Failure != nil {
		if validateErr := validateBrokerResult(result, input.AKA); validateErr != nil {
			writeBrokerError(response, http.StatusBadGateway, RemoteError{Kind: "failed", Code: "invalid_agent_result"})
			return
		}
		writeBrokerJSON(response, http.StatusOK, result)
		return
	}
	if err == nil {
		if validateErr := validateBrokerResult(result, input.AKA); validateErr != nil {
			writeBrokerError(response, http.StatusBadGateway, RemoteError{Kind: "failed", Code: "invalid_agent_result"})
			return
		}
		writeBrokerJSON(response, http.StatusOK, result)
		return
	}
	switch {
	case errors.Is(err, ErrCardOffline):
		writeBrokerError(response, http.StatusServiceUnavailable, RemoteError{Kind: "not_ready", Code: "card_offline", Retryable: true})
	case errors.Is(err, ErrCardAmbiguous):
		writeBrokerError(response, http.StatusConflict, RemoteError{Kind: "conflict", Code: "card_identity_ambiguous"})
	case errors.Is(err, ErrAgentOffline):
		writeBrokerError(response, http.StatusServiceUnavailable, RemoteError{Kind: "not_ready", Code: "agent_offline", Retryable: true})
	case errors.Is(err, ErrGenerationMismatch):
		writeBrokerError(response, http.StatusConflict, RemoteError{Kind: "conflict", Code: "agent_generation_mismatch"})
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeBrokerError(response, http.StatusGatewayTimeout, RemoteError{Kind: "transport", Code: "agent_operation_timeout", Retryable: true})
	default:
		writeBrokerError(response, http.StatusBadGateway, RemoteError{Kind: "failed", Code: "agent_broker_failed", Retryable: true})
	}
}

func validateBrokerResult(result AKAResponse, challenge AKAChallenge) error {
	if result.SessionGeneration == "" {
		return errors.New("Agent result has no resolved session generation")
	}
	return result.ValidateFor(challenge.requestFor(result.SessionGeneration))
}

func (api *BrokerAPI) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(presented[:], api.tokenHash[:]) == 1
}

func literalLoopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func decodeBrokerJSON(body io.Reader, target any) error {
	limited := io.LimitReader(body, maximumBrokerRequest+1)
	payload, err := io.ReadAll(limited)
	if err != nil || len(payload) == 0 || len(payload) > maximumBrokerRequest {
		return errors.New("invalid broker request size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("broker request has trailing JSON")
	}
	return nil
}

func writeBrokerError(response http.ResponseWriter, status int, failure RemoteError) {
	writeBrokerJSON(response, status, struct {
		Failure RemoteError `json:"failure"`
	}{Failure: failure})
}

func writeBrokerJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
