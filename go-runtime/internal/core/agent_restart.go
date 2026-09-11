package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

type AgentRestartRuntime interface {
	AgentFacts
	RequestAgentRestart(context.Context, string, agentlink.AgentRestartRequest) (agentlink.AgentRestartResponse, error)
}

// Prepare must exclude new business operations until its release callback is
// called. The handler never substitutes a display-only idle flag for a permit.
type AgentRestartGuard interface {
	Prepare(context.Context, agentlink.ConnectionStatus, string) (func(context.Context) error, error)
}

type AgentRestartHandler struct {
	agents AgentRestartRuntime
	guard  AgentRestartGuard
	busy   sync.Map
	wait   func(context.Context, time.Duration) error
}

func NewAgentRestartHandler(agents AgentRestartRuntime, guard AgentRestartGuard) (*AgentRestartHandler, error) {
	if agents == nil || guard == nil {
		return nil, errors.New("Agent restart requires runtime and business guard")
	}
	return &AgentRestartHandler{agents: agents, guard: guard, wait: waitRestartInterval}, nil
}

func waitRestartInterval(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (handler *AgentRestartHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	var input agentlink.AgentRestartRequest
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.Validate() != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_agent_restart"})
		return
	}
	id := strings.TrimSpace(request.PathValue("agentID"))
	if _, busy := handler.busy.LoadOrStore(id, true); busy {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "agent_restart_pending"})
		return
	}
	defer handler.busy.Delete(id)
	status, ok := handler.agents.Status(id)
	if !ok || status.ProcessGeneration != input.ProcessGeneration {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "agent_generation_changed_or_offline"})
		return
	}
	if !hasAgentFeature(status.Capabilities, agentlink.AgentRestartFeature) {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "agent_restart_unavailable"})
		return
	}
	// Once accepted, a browser disconnect must not abandon the maintenance permit.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 4*time.Minute)
	defer cancel()
	release, err := handler.guard.Prepare(ctx, status, input.OperationID)
	if err != nil || release == nil {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "agent_restart_not_safe"})
		return
	}
	defer func() {
		if release != nil {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			_ = release(cleanup)
		}
	}()
	result, restartErr := handler.agents.RequestAgentRestart(ctx, id, input)
	newGeneration := ""
	if restartErr == nil && result.Accepted {
		for _, delay := range []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second} {
			restartErr = handler.wait(ctx, delay)
			if restartErr != nil {
				break
			}
			current, exists := handler.agents.Status(id)
			if exists && current.ProcessGeneration != input.ProcessGeneration && current.Topology != nil && !current.LastReport.IsZero() {
				newGeneration = current.ProcessGeneration
				break
			}
		}
	}
	releaseContext, releaseCancel := context.WithTimeout(context.Background(), 30*time.Second)
	releaseErr := release(releaseContext)
	release = nil
	releaseCancel()
	if releaseErr != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "agent_restart_maintenance_release_failed", "operation_id": input.OperationID})
		return
	}
	if restartErr != nil || newGeneration == "" {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "agent_restart_unconfirmed", "operation_id": input.OperationID})
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"state": "restarted", "operation_id": input.OperationID, "process_generation": newGeneration})
}

func hasAgentFeature(features []string, target string) bool {
	for _, feature := range features {
		if feature == target {
			return true
		}
	}
	return false
}
