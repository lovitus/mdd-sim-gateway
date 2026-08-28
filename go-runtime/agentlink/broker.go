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
	AuthenticateAKA(context.Context, string, string, AKARequest) (AKAResponse, error)
}

type BrokerRequest struct {
	AgentID           string     `json:"agent_id"`
	ProcessGeneration string     `json:"process_generation"`
	AKA               AKARequest `json:"aka"`
}

func (request BrokerRequest) Validate() error {
	if !validIdentifier(request.AgentID) || !validIdentifier(request.ProcessGeneration) {
		return errors.New("invalid broker Agent identity or process generation")
	}
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
	result, err := api.broker.AuthenticateAKA(
		ctx, input.AgentID, input.ProcessGeneration, input.AKA,
	)
	cancel()
	if result.Failure != nil {
		if validateErr := result.ValidateFor(input.AKA); validateErr != nil {
			writeBrokerError(response, http.StatusBadGateway, RemoteError{Kind: "failed", Code: "invalid_agent_result"})
			return
		}
		writeBrokerJSON(response, http.StatusOK, result)
		return
	}
	if err == nil {
		if validateErr := result.ValidateFor(input.AKA); validateErr != nil {
			writeBrokerError(response, http.StatusBadGateway, RemoteError{Kind: "failed", Code: "invalid_agent_result"})
			return
		}
		writeBrokerJSON(response, http.StatusOK, result)
		return
	}
	switch {
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
