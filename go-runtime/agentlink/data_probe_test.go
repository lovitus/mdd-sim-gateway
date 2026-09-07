package agentlink

import (
	"context"
	"strings"
	"testing"
)

func TestPolicyProbeRejectsAgentWithoutNegotiatedSupport(t *testing.T) {
	server := &Server{agents: map[string]*serverConnection{"agent": {
		hello: Hello{ProcessGeneration: "generation"}, capabilities: []string{modemDataRenewFeature, modemPolicyFeature},
	}}}
	_, err := server.ExecuteModemData(context.Background(), "agent", "generation", ModemDataRequest{
		OperationID: "stop-probe", AttachmentID: "attachment", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "session", Action: ModemDataStop, SessionID: "data-session", Purpose: "probe:once",
	})
	if err == nil || !strings.Contains(err.Error(), "cellular_data_policy_probe_unsupported") {
		t.Fatalf("old Agent probe error: %v", err)
	}
}
