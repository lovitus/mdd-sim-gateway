package agentlink

import "errors"

var ErrAgentMaintenance = errors.New("Agent is in host maintenance")

// BeginHostMaintenance atomically excludes new RPCs after all current RPCs
// finish. The Agent-ID scope survives its reconnect until explicit release.
func (server *Server) BeginHostMaintenance(agentID, generation, leaseID string) error {
	if !validIdentifier(leaseID) || agentID == "" || generation == "" {
		return errors.New("invalid Agent maintenance identity")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
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
