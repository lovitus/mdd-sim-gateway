package agentlink

import (
	"context"
	"errors"
)

type euiccMaintenanceKey struct{}
type euiccMaintenanceCall struct {
	agent, generation, lease string
	request                  EUICCProfileRequest
}

// WithEUICCProfileMaintenance is Core-local authority for one frozen command.
// It is never populated from a header, browser request or Agent wire field.
func WithEUICCProfileMaintenance(ctx context.Context, agent, generation, lease string, request EUICCProfileRequest) context.Context {
	return context.WithValue(ctx, euiccMaintenanceKey{}, euiccMaintenanceCall{agent, generation, lease, request})
}

func permittedEUICC(ctx context.Context, connection *serverConnection, lease string, message envelope) bool {
	permit, ok := ctx.Value(euiccMaintenanceKey{}).(euiccMaintenanceCall)
	return ok && lease != "" && permit.lease == lease && permit.agent == connection.hello.AgentID &&
		permit.generation == connection.hello.ProcessGeneration && message.Kind == kindEUICCRequest &&
		message.EUICCRequest != nil && *message.EUICCRequest == permit.request
}

var ErrAgentMaintenance = errors.New("Agent is in host maintenance")

// BeginHostMaintenance atomically excludes new RPCs after all current RPCs
// finish. The Agent-ID scope survives its reconnect until explicit release.
func (server *Server) BeginHostMaintenance(agentID, generation, leaseID string) error {
	if !validIdentifier(leaseID) || agentID == "" || generation == "" {
		return errors.New("invalid Agent maintenance identity")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.updateMaintenance != "" {
		return ErrAgentMaintenance
	}
	if current := server.maintenance[agentID]; current != "" {
		if current == leaseID {
			return nil
		}
		return ErrAgentMaintenance
	}
	connection := server.agents[agentID]
	if connection == nil {
		return ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != generation {
		return ErrGenerationMismatch
	}
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if len(connection.pending) != 0 {
		return errors.New("Agent requests are still active")
	}
	if server.maintenance == nil {
		server.maintenance = make(map[string]string)
	}
	server.maintenance[agentID] = leaseID
	return nil
}

// BeginUpdateMaintenance is installed only after Core's business admission is
// closed and every existing session is confirmed idle. The same lock fences
// pending RPC registration, including Agents connecting after this call.
func (server *Server) BeginUpdateMaintenance(leaseID string) error {
	if !validIdentifier(leaseID) {
		return errors.New("invalid update maintenance lease")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.updateMaintenance == leaseID {
		return nil
	}
	if server.updateMaintenance != "" || len(server.maintenance) != 0 {
		return ErrAgentMaintenance
	}
	for _, connection := range server.agents {
		connection.mu.Lock()
		busy := len(connection.pending) != 0
		connection.mu.Unlock()
		if busy {
			return ErrAgentMaintenance
		}
	}
	server.updateMaintenance = leaseID
	return nil
}

func (server *Server) EndUpdateMaintenance(leaseID string) error {
	server.mu.Lock()
	defer server.mu.Unlock()
	if leaseID == "" || server.updateMaintenance != "" && server.updateMaintenance != leaseID {
		return ErrAgentMaintenance
	}
	server.updateMaintenance = ""
	return nil
}

func (server *Server) EndHostMaintenance(agentID, leaseID string) error {
	if !validIdentifier(leaseID) || agentID == "" {
		return errors.New("invalid Agent maintenance identity")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if current := server.maintenance[agentID]; current != "" && current != leaseID {
		return ErrAgentMaintenance
	}
	delete(server.maintenance, agentID)
	return nil
}
