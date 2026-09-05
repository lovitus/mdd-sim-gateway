package agentlink

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeProvisionExecutor struct{}

func (fakeProvisionExecutor) ExecuteProvision(_ context.Context, request ProvisionRequest) ProvisionResponse {
	return appliedProvisionResponse(request)
}

func (fakeProvisionExecutor) ReconcileProvision(_ context.Context, request ProvisionRequest) ProvisionResponse {
	return appliedProvisionResponse(request)
}

func appliedProvisionResponse(request ProvisionRequest) ProvisionResponse {
	return ProvisionResponse{
		OperationID: request.OperationID, State: ProvisionApplied,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
		SIMSessionGeneration: request.SIMSessionGeneration,
	}
}

func validProvisionCommand() ProvisionCommand {
	return ProvisionCommand{
		OperationID: "provision-1", LineID: "line-1", EquipmentID: "862547055201716",
		CardID: "89010000000000000001", AttachmentID: "attach-1",
		SIMSessionGeneration: "sim-1", IMSI: "460001234567890",
		MCC: "460", MNC: "01", IMEI: "356789012345678",
		SMSC: "+8613800138000",
	}
}

func TestProvisionCommandValidate(t *testing.T) {
	if err := validProvisionCommand().Validate(); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	tests := []struct {
		name string
		mut  func(*ProvisionCommand)
	}{
		{"missing smsc", func(command *ProvisionCommand) { command.SMSC = "" }},
		{"bad imei", func(command *ProvisionCommand) { command.IMEI = "123" }},
		{"bad imsi", func(command *ProvisionCommand) { command.IMSI = "46001x" }},
		{"missing generation", func(command *ProvisionCommand) { command.SIMSessionGeneration = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := validProvisionCommand()
			test.mut(&command)
			if err := command.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestProvisionResponseValidate(t *testing.T) {
	response := ProvisionResponse{
		OperationID: "provision-1", State: ProvisionApplied,
		EquipmentID: "862547055201716", CardID: "89010000000000000001",
		SIMSessionGeneration: "sim-1",
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	response.State = ProvisionFailed
	if err := response.Validate(); err == nil {
		t.Fatal("failed response without error code accepted")
	}
	response.ErrorCode = "pin_required"
	if err := response.Validate(); err != nil {
		t.Fatalf("failed response rejected: %v", err)
	}
	response.State = ProvisionUnknown
	response.ErrorCode = ""
	if err := response.Validate(); err == nil {
		t.Fatal("unknown response without error code accepted")
	}
}

func TestProvisionEnvelopeRoundTrip(t *testing.T) {
	command := validProvisionCommand()
	input := envelope{Kind: kindProvisionRequest, RequestID: "request-1", ProvisionRequest: &ProvisionRequest{ProvisionCommand: command}}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var decoded envelope
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.validate(); err != nil {
		t.Fatalf("request envelope rejected: %v", err)
	}
	decoded.ProvisionRequest = nil
	decoded.ProvisionResult = &ProvisionResponse{
		OperationID: command.OperationID, State: ProvisionUnknown,
		EquipmentID: command.EquipmentID, CardID: command.CardID,
		SIMSessionGeneration: command.SIMSessionGeneration,
		ErrorCode:            "hardware_executor_unavailable",
	}
	decoded.Kind = kindProvisionResponse
	if err := decoded.validate(); err != nil {
		t.Fatalf("response envelope rejected: %v", err)
	}
}

func TestProvisionReconcileUsesAgentWSS(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL:   strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/v1/agent/ws",
			Token: testToken, Hello: Hello{SchemaVersion: SchemaVersion, AgentID: "agent-1", ProcessGeneration: "process-1"},
			Authenticator: &fakeAuthenticator{}, Provision: fakeProvisionExecutor{}, Health: func() TopologySnapshot {
				return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}}
			},
			HealthEvery: time.Second, OperationTimeout: time.Second,
		}).Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, found := server.Status("agent-1"); found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provision Agent did not connect")
		}
		time.Sleep(time.Millisecond)
	}
	request := ProvisionRequest{ProvisionCommand: validProvisionCommand()}
	result, err := server.ReconcileProvision(context.Background(), "agent-1", "process-1", request)
	if err != nil || result.State != ProvisionApplied {
		var clientErr error
		select {
		case clientErr = <-done:
		default:
		}
		t.Fatalf("ReconcileProvision() result=%+v error=%v client_error=%v", result, err, clientErr)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("provision Agent did not stop")
	}
}
