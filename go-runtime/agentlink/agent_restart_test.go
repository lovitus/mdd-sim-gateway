package agentlink

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAgentRestartEnvelopeAndResponseFence(t *testing.T) {
	request := AgentRestartRequest{OperationID: "restart-a", ProcessGeneration: "process-a"}
	message := envelope{Kind: kindAgentRestartRequest, RequestID: "wire-a", AgentRestartRequest: &request}
	if err := message.validate(); err != nil {
		t.Fatal(err)
	}
	message.Hello = &Hello{SchemaVersion: 1, AgentID: "agent-a", ProcessGeneration: "process-a"}
	if message.validate() == nil {
		t.Fatal("foreign payload accepted with restart request")
	}
	message.Hello = nil
	message.Kind = kindHelloAck
	if message.validate() == nil {
		t.Fatal("restart payload accepted on another message kind")
	}
	response := AgentRestartResponse{OperationID: request.OperationID, ProcessGeneration: request.ProcessGeneration, Accepted: true}
	if err := response.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
	response.ProcessGeneration = "process-b"
	if response.ValidateFor(request) == nil {
		t.Fatal("replacement process accepted as the original target")
	}
	response.ProcessGeneration = request.ProcessGeneration
	response.Failure = &RemoteError{Kind: "conflict", Code: "agent_busy"}
	if response.ValidateFor(request) == nil {
		t.Fatal("accepted and failed response allowed")
	}
	response.Accepted = false
	if err := response.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
}

func TestRestartUsesNegotiatedTransportAndRejectsOldGeneration(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	committed := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- (Client{URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent",
			Token: testToken, Hello: Hello{SchemaVersion: 1, AgentID: "restart-agent", ProcessGeneration: "current"},
			Authenticator: &fakeAuthenticator{}, OperationTimeout: time.Second,
			PrepareRestart: func(context.Context, AgentRestartRequest) (func(context.Context) error, error) {
				return func(context.Context) error { close(committed); return errors.New("test supervisor") }, nil
			},
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	waitForHealth(t, server, "restart-agent", func(status ConnectionStatus) bool {
		return featureEnabled(strings.Join(status.Capabilities, ","), AgentRestartFeature)
	})
	request := AgentRestartRequest{OperationID: "op", ProcessGeneration: "old"}
	if _, err := server.RequestAgentRestart(ctx, "restart-agent", request); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("old generation: %v", err)
	}
	select {
	case <-committed:
		t.Fatal("old generation restarted target")
	default:
	}
	request.ProcessGeneration = "current"
	if err := server.BeginHostMaintenance("restart-agent", "current", request.OperationID); err != nil {
		t.Fatal(err)
	}
	defer server.EndHostMaintenance("restart-agent", request.OperationID)
	result, err := server.RequestAgentRestart(ctx, "restart-agent", request)
	if err != nil || !result.Accepted {
		t.Fatalf("restart acknowledgement: %+v %v", result, err)
	}
	select {
	case <-committed:
	case <-ctx.Done():
		t.Fatal("accepted restart was not dispatched")
	}
}
