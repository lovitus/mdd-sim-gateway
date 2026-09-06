package agentlink

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeRecoveryExecutor struct{ requests []ModemRecoveryRequest }

func (executor *fakeRecoveryExecutor) ExecuteModemRecovery(_ context.Context,
	request ModemRecoveryRequest,
) ModemRecoveryResponse {
	executor.requests = append(executor.requests, request)
	return ModemRecoveryResponse{OperationID: request.OperationID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, AttachmentID: request.AttachmentID, SIMSessionGeneration: request.SIMSessionGeneration,
		Action: request.Action, State: "accepted"}
}

func TestModemRecoveryUsesNegotiatedWSSAndExactSession(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	executor := &fakeRecoveryExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent",
			Token: testToken, Hello: Hello{SchemaVersion: 1, AgentID: "agent-1", ProcessGeneration: "process-1"},
			Authenticator: &fakeAuthenticator{}, Recovery: executor, OperationTimeout: time.Second}).Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if status, found := server.Status("agent-1"); found && featureEnabled(strings.Join(status.Capabilities, ","), modemRecoveryFeature) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery Agent did not connect")
		}
		time.Sleep(time.Millisecond)
	}
	request := ModemRecoveryRequest{ModemRecoveryCommand: ModemRecoveryCommand{OperationID: "restart-1",
		EquipmentID: "862547055201716", CardID: "8985200000000000001", Action: ModemSoftRestart},
		ProcessGeneration: "process-1", AttachmentID: "attachment-1", SIMSessionGeneration: "session-1"}
	result, err := server.ExecuteModemRecovery(context.Background(), "agent-1", "process-1", request)
	if err != nil || result.State != "accepted" || len(executor.requests) != 1 || executor.requests[0] != request {
		t.Fatalf("result=%+v requests=%+v err=%v", result, executor.requests, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery Agent did not stop")
	}
}

func TestModemRecoveryTargetRequiresFreshUniqueCard(t *testing.T) {
	server := modemCardTestServer(t)
	fact := modemCardFact("attachment-1", "862547055201716", "8985200000000000001", false, true, false)
	connection := modemCardConnection("agent-1", "process-1", fact)
	connection.capabilities = append(connection.capabilities, modemRecoveryFeature)
	server.agents["agent-1"] = connection
	target, err := server.ResolveModemRecoveryTarget(fact.EquipmentID, fact.SIM.ICCID)
	if err != nil || target.SIMSessionGeneration != fact.SIM.SessionGeneration {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	connection.lastReport = time.Now().Add(-31 * time.Second)
	if _, err := server.ResolveModemRecoveryTarget(fact.EquipmentID, fact.SIM.ICCID); !errors.Is(err, ErrModemOffline) {
		t.Fatalf("stale target error=%v", err)
	}
	connection.lastReport = time.Now()
	server.agents["reader"] = &serverConnection{hello: Hello{AgentID: "reader", ProcessGeneration: "reader-process"},
		lastReport: time.Now(), topology: &TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
			ReaderName: "reader", CardPresent: true, IdentityState: CardIdentified,
			CardID: fact.SIM.ICCID, SessionGeneration: "reader-session"}}}}
	if _, err := server.ResolveModemRecoveryTarget(fact.EquipmentID, fact.SIM.ICCID); !errors.Is(err, ErrModemAmbiguous) {
		t.Fatalf("duplicate target error=%v", err)
	}
}
