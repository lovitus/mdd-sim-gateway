package agentlink

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeAuthenticator struct {
	mu       sync.Mutex
	requests []AKARequest
	failure  *RemoteError
	wait     <-chan struct{}
}

func (fake *fakeAuthenticator) AuthenticateAKA(ctx context.Context, request AKARequest) AKAResponse {
	if fake.wait != nil {
		select {
		case <-ctx.Done():
			return AKAResponse{
				OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
				Failure: &RemoteError{Kind: "transport", Code: "operation_timeout", Retryable: true},
			}
		case <-fake.wait:
		}
	}
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	failure := fake.failure
	fake.mu.Unlock()
	if failure != nil {
		copy := *failure
		return AKAResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			Failure: &copy,
		}
	}
	return AKAResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
		Body: []byte{0xdb, 0x04, 1, 2, 3, 4}, SW1: 0x90, SW2: 0x00,
	}
}

func TestAgentLinkRoundTripAndGenerationBoundary(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(_ context.Context, agentID string) (string, error) {
		if agentID != "agent-1" {
			return "", errors.New("unknown Agent")
		}
		return testToken, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	authenticator := &fakeAuthenticator{}
	acknowledged := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- (Client{
			URL:           strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/v1/agent/connect",
			Token:         testToken,
			Hello:         Hello{SchemaVersion: SchemaVersion, AgentID: "agent-1", ProcessGeneration: "process-1"},
			Authenticator: authenticator, OperationTimeout: time.Second,
			Connected: func() { close(acknowledged) },
		}).Run(ctx)
	}()
	select {
	case <-acknowledged:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent hello was not acknowledged")
	}
	defer func() {
		cancel()
		select {
		case <-clientDone:
		case <-time.After(2 * time.Second):
			t.Fatal("Agent client did not stop")
		}
	}()

	request := AKARequest{
		OperationID: "aka-1", SessionGeneration: "card-session-7", CardID: "8944000000000000001",
		Application: AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	}
	var response AKAResponse
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err = server.AuthenticateAKA(context.Background(), "agent-1", "process-1", request)
		if !errors.Is(err, ErrAgentOffline) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatalf("AuthenticateAKA() error = %v", err)
	}
	if response.SW1 != 0x90 || response.SW2 != 0 || len(response.Body) == 0 {
		t.Fatalf("AuthenticateAKA() = %+v", response)
	}
	status, connected := server.Status("agent-1")
	if !connected || status.ProcessGeneration != "process-1" || status.LastSeen.Before(status.ConnectedAt) {
		t.Fatalf("Agent connection status = %+v connected=%v", status, connected)
	}
	if _, err := server.AuthenticateAKA(context.Background(), "agent-1", "old-process", request); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("generation mismatch error = %v", err)
	}
}

func TestAgentLinkPreservesTypedFailure(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	authenticator := &fakeAuthenticator{failure: &RemoteError{
		Kind: "not_ready", Code: "card_session_replaced", Retryable: true, RetryAfter: 250,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = (Client{
			URL:   strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent",
			Token: testToken, Hello: Hello{SchemaVersion: 1, AgentID: "agent-2", ProcessGeneration: "p-2"},
			Authenticator: authenticator, OperationTimeout: time.Second,
		}).Run(ctx)
	}()
	request := AKARequest{
		OperationID: "aka-2", SessionGeneration: "session-2", CardID: "8902",
		Application: AKAApplicationISIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err := server.AuthenticateAKA(context.Background(), "agent-2", "p-2", request)
		if errors.Is(err, ErrAgentOffline) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
			continue
		}
		var remote *RemoteError
		if !errors.As(err, &remote) || remote.Code != "card_session_replaced" || response.Failure == nil {
			t.Fatalf("response=%+v error=%v", response, err)
		}
		break
	}
}

func TestLateResponseDoesNotDisconnectAgent(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	wait := make(chan struct{})
	authenticator := &fakeAuthenticator{wait: wait}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL:   strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent",
			Token: testToken, Hello: Hello{SchemaVersion: 1, AgentID: "late-agent", ProcessGeneration: "late-process"},
			Authenticator: authenticator, OperationTimeout: time.Second,
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	request := AKARequest{
		OperationID: "late-1", SessionGeneration: "late-session", CardID: "1",
		Application: AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		requestContext, stopRequest := context.WithTimeout(context.Background(), 5*time.Millisecond)
		_, err := server.AuthenticateAKA(requestContext, "late-agent", "late-process", request)
		stopRequest()
		if errors.Is(err, ErrAgentOffline) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
			continue
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timed request error = %v", err)
		}
		break
	}
	close(wait)
	request.OperationID = "late-2"
	response, err := server.AuthenticateAKA(context.Background(), "late-agent", "late-process", request)
	if err != nil || response.SW1 != 0x90 {
		t.Fatalf("request after late response = %+v, %v", response, err)
	}
}

func TestAgentLinkRejectsInsecureRemoteWSAndInvalidMessages(t *testing.T) {
	validHello := Hello{SchemaVersion: 1, AgentID: "agent", ProcessGeneration: "process"}
	client := Client{
		URL: "ws://192.0.2.1/agent", Token: testToken, Hello: validHello,
		Authenticator: &fakeAuthenticator{}, OperationTimeout: time.Second,
	}
	if err := client.validate(); err == nil {
		t.Fatal("remote plaintext ws unexpectedly accepted")
	}
	for name, request := range map[string]AKARequest{
		"missing card identity": {OperationID: "op", SessionGeneration: "s", Application: AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16)},
		"short RAND":            {OperationID: "op", SessionGeneration: "s", CardID: "1", Application: AKAApplicationUSIM, RAND: make([]byte, 15), AUTN: make([]byte, 16)},
		"unsupported app":       {OperationID: "op", SessionGeneration: "s", CardID: "1", Application: "csim", RAND: make([]byte, 16), AUTN: make([]byte, 16)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestAgentLinkRejectsUnknownAndTrailingJSON(t *testing.T) {
	for name, payload := range map[string]string{
		"unknown":  `{"kind":"hello","hello":{"schema_version":1,"agent_id":"a","process_generation":"p"},"future":true}`,
		"trailing": `{"kind":"hello","hello":{"schema_version":1,"agent_id":"a","process_generation":"p"}} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeEnvelope([]byte(payload)); err == nil {
				t.Fatal("invalid JSON message accepted")
			}
		})
	}
}
