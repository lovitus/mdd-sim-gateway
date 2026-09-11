package agentlink

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

const AgentRestartFeature = "agent-process-restart-v1"
const kindAgentRestartRequest = "agent_restart_request"
const kindAgentRestartResponse = "agent_restart_response"

type AgentRestartRequest struct {
	OperationID       string `json:"operation_id"`
	ProcessGeneration string `json:"process_generation"`
}

func (request AgentRestartRequest) Validate() error {
	if !validIdentifier(request.OperationID) || !validIdentifier(request.ProcessGeneration) {
		return errors.New("invalid Agent restart identity")
	}
	return nil
}

type AgentRestartResponse struct {
	OperationID       string       `json:"operation_id"`
	ProcessGeneration string       `json:"process_generation"`
	Accepted          bool         `json:"accepted"`
	Failure           *RemoteError `json:"failure,omitempty"`
}

func (response AgentRestartResponse) ValidateFor(request AgentRestartRequest) error {
	if response.OperationID != request.OperationID || response.ProcessGeneration != request.ProcessGeneration ||
		response.Accepted == (response.Failure != nil) {
		return errors.New("Agent restart response does not match request")
	}
	if response.Failure != nil {
		return response.Failure.Validate()
	}
	return nil
}

func validateAgentRestartEnvelope(message envelope) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil || len(fields) != 3 || !validIdentifier(message.RequestID) {
		return errors.New("invalid Agent restart envelope")
	}
	if message.Kind == kindAgentRestartRequest && message.AgentRestartRequest != nil && message.AgentRestartResult == nil {
		return message.AgentRestartRequest.Validate()
	}
	if message.Kind == kindAgentRestartResponse && message.AgentRestartResult != nil && message.AgentRestartRequest == nil {
		result := message.AgentRestartResult
		request := AgentRestartRequest{OperationID: result.OperationID, ProcessGeneration: result.ProcessGeneration}
		if err := request.Validate(); err != nil {
			return err
		}
		return result.ValidateFor(request)
	}
	return errors.New("missing Agent restart payload")
}

func (server *Server) RequestAgentRestart(ctx context.Context, agentID string, request AgentRestartRequest) (AgentRestartResponse, error) {
	if err := request.Validate(); err != nil {
		return AgentRestartResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return AgentRestartResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != request.ProcessGeneration {
		return AgentRestartResponse{}, ErrGenerationMismatch
	}
	if !featureEnabled(strings.Join(connection.capabilities, ","), AgentRestartFeature) {
		return AgentRestartResponse{}, errors.New("Agent process restart is unavailable")
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindAgentRestartRequest, AgentRestartRequest: &request})
	if err != nil {
		return AgentRestartResponse{}, err
	}
	if message.AgentRestartResult == nil {
		return AgentRestartResponse{}, errors.New("missing Agent restart result")
	}
	result := *message.AgentRestartResult
	if err := result.ValidateFor(request); err != nil {
		return AgentRestartResponse{}, err
	}
	if result.Failure != nil {
		return result, result.Failure
	}
	return result, nil
}
