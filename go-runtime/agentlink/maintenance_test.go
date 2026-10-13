package agentlink

import (
	"context"
	"errors"
	"testing"
)

func TestHostMaintenanceExcludesRPCsAcrossReconnect(t *testing.T) {
	connection := &serverConnection{hello: Hello{AgentID: "host", ProcessGeneration: "old"}, pending: map[string]chan envelope{}, closed: make(chan struct{})}
	server := &Server{agents: map[string]*serverConnection{"host": connection}}
	connection.pending["busy"] = make(chan envelope, 1)
	if err := server.BeginHostMaintenance("host", "old", "lease-one"); err == nil {
		t.Fatal("pending request ignored")
	}
	delete(connection.pending, "busy")
	if err := server.BeginHostMaintenance("host", "old", "lease-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.roundTrip(context.Background(), connection, envelope{}); !errors.Is(err, ErrAgentMaintenance) {
		t.Fatal("RPC admitted during maintenance", err)
	}
	connection.hello.ProcessGeneration = "new"
	if _, err := server.roundTrip(context.Background(), connection, envelope{}); !errors.Is(err, ErrAgentMaintenance) {
		t.Fatal("reconnect bypassed maintenance", err)
	}
	if err := server.EndHostMaintenance("host", "other-lease"); !errors.Is(err, ErrAgentMaintenance) {
		t.Fatal("different lease released host", err)
	}
	if err := server.EndHostMaintenance("host", "lease-one"); err != nil {
		t.Fatal(err)
	}
	if len(server.maintenance) != 0 {
		t.Fatal("host remained gated")
	}
}

func TestUpdateMaintenanceFencesNewAgentsButNotRefusedBusyAdmission(t *testing.T) {
	connection := &serverConnection{hello: Hello{AgentID: "host", ProcessGeneration: "old"}, pending: map[string]chan envelope{"busy": make(chan envelope, 1)}, closed: make(chan struct{})}
	server := &Server{agents: map[string]*serverConnection{"host": connection}}
	if err := server.BeginUpdateMaintenance("update-one"); err == nil || server.updateMaintenance != "" {
		t.Fatal("BUSY_RPC: update disturbed existing owner")
	}
	delete(connection.pending, "busy")
	if err := server.BeginUpdateMaintenance("update-one"); err != nil {
		t.Fatal(err)
	}
	newcomer := &serverConnection{hello: Hello{AgentID: "new-host", ProcessGeneration: "new"}, pending: map[string]chan envelope{}, closed: make(chan struct{})}
	server.agents["new-host"] = newcomer
	for _, current := range []*serverConnection{connection, newcomer} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := server.roundTrip(ctx, current, envelope{}); !errors.Is(err, ErrAgentMaintenance) {
			t.Fatalf("NEW_AGENT_RPC: maintenance bypassed: %v", err)
		}
	}
	if err := server.BeginHostMaintenance("new-host", "new", "foreign"); !errors.Is(err, ErrAgentMaintenance) {
		t.Fatal("host lease bypassed update")
	}
	if err := server.EndUpdateMaintenance("foreign"); !errors.Is(err, ErrAgentMaintenance) {
		t.Fatal("foreign operation released update")
	}
	if err := server.EndUpdateMaintenance("update-one"); err != nil || server.updateMaintenance != "" {
		t.Fatal("original update did not release")
	}
}
