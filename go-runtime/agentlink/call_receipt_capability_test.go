package agentlink

import (
	"errors"
	"testing"
)

func TestCallReceiptNeverDispatchesToUnnegotiatedAgent(t *testing.T) {
	connection := &serverConnection{hello: Hello{AgentID: "agent-1", ProcessGeneration: "old-generation"}, closed: make(chan struct{}), pending: map[string]chan envelope{}}
	server := &Server{agents: map[string]*serverConnection{"agent-1": connection}}
	_, err := server.ExecuteModem(t.Context(), "agent-1", "old-generation", ModemRequest{OperationID: "original-start", AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001", LeaseID: "original-lease", Action: ModemCallReceipt})
	var failure *RemoteError
	if !errors.As(err, &failure) || failure.Code != "modem_call_receipt_unsupported" {
		t.Fatalf("unsupported receipt=%v", err)
	}
	if server.nextID.Load() != 0 || len(connection.pending) != 0 {
		t.Fatal("unsupported operation reached wire dispatch")
	}
	select {
	case <-connection.closed:
		t.Fatal("unsupported receipt retired the Agent connection")
	default:
	}
	_, err = server.ExecuteModem(t.Context(), "agent-1", "old-generation", ModemRequest{OperationID: "end-original", AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001", LeaseID: "original-lease", Action: ModemCallHangup})
	if !errors.As(err, &failure) || failure.Code != "modem_scoped_hangup_unsupported" || server.nextID.Load() != 0 {
		t.Fatalf("scoped hangup reached legacy wire: %v", err)
	}
}
