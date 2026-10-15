package agentlink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeReaderReadbackExecutor struct{}

func (fakeReaderReadbackExecutor) ReadReader(_ context.Context, request ReaderReadbackRequest) ReaderReadbackResponse {
	return validReaderReadbackResponse(request)
}

func validReaderReadbackRequest() ReaderReadbackRequest {
	return ReaderReadbackRequest{
		OperationID: "reader-readback-1", ProcessGeneration: "process-1",
		ReaderName: "reader-a", CardID: "8944000000000000001",
		SIMSessionGeneration: "session-1",
	}
}

func validReaderReadbackResponse(request ReaderReadbackRequest) ReaderReadbackResponse {
	return ReaderReadbackResponse{
		Scope:       request.Scope,
		OperationID: request.OperationID, ProcessGeneration: request.ProcessGeneration,
		ReaderName: request.ReaderName, CardID: request.CardID,
		SIMSessionGeneration: request.SIMSessionGeneration, State: "applied",
		Reader: &ReaderFact{
			ReaderName: request.ReaderName, CardPresent: true,
			SessionGeneration: request.SIMSessionGeneration, CardID: request.CardID,
			IdentityState: CardIdentified,
		},
	}
}

func TestReaderSIMIdentityResponseScopeAndInnerIdentity(t *testing.T) {
	request := validReaderReadbackRequest()
	for _, state := range []string{"applied", "unknown", "failed"} {
		t.Run(state, func(t *testing.T) {
			request.Scope = ReaderReadbackSIMIdentity
			response := validReaderReadbackResponse(request)
			response.State = state
			if state != "applied" {
				response.Reader = nil
				response.ErrorCode = "readback_failed"
			}
			if err := response.ValidateFor(request); err != nil {
				t.Fatal(err)
			}
			response.Scope = ""
			if response.ValidateFor(request) == nil {
				t.Fatal("scope echo missing")
			}
		})
	}
	for _, field := range []string{"reader", "card", "generation", "euicc"} {
		response := validReaderReadbackResponse(request)
		switch field {
		case "reader":
			response.Reader.ReaderName = "other-reader"
		case "card":
			response.Reader.CardID = "8944000000000000002"
		case "generation":
			response.Reader.SessionGeneration = "other-session"
		case "euicc":
			response.Reader.EUICC = &EUICCFact{}
		}
		if response.ValidateFor(request) == nil {
			t.Fatalf("accepted invalid %s observation", field)
		}
	}
	request.Scope = ""
	for _, value := range []any{request, validReaderReadbackResponse(request)} {
		data, _ := json.Marshal(value)
		var fields map[string]any
		_ = json.Unmarshal(data, &fields)
		if _, found := fields["scope"]; found {
			t.Fatal("empty scope breaks old strict decoder")
		}
	}
}

type readerReadbackFunc func(context.Context, ReaderReadbackRequest) ReaderReadbackResponse

func (fn readerReadbackFunc) ReadReader(ctx context.Context, request ReaderReadbackRequest) ReaderReadbackResponse {
	return fn(ctx, request)
}

type scopedReaderReadbackExecutor struct{ readerReadbackFunc }

func (scopedReaderReadbackExecutor) SupportsReaderSIMIdentity() bool { return true }

func TestReaderSIMIdentityNegotiationDoesNotDispatchToLegacyAgent(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "scoped"}[capable], func(t *testing.T) {
			server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
			if err != nil {
				t.Fatal(err)
			}
			host := httptest.NewServer(server)
			defer host.Close()
			var calls atomic.Int32
			fn := readerReadbackFunc(func(_ context.Context, request ReaderReadbackRequest) ReaderReadbackResponse {
				calls.Add(1)
				return validReaderReadbackResponse(request)
			})
			var executor ReaderReadbackExecutor = fn
			if capable {
				executor = scopedReaderReadbackExecutor{fn}
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- (Client{URL: strings.Replace(host.URL, "http://", "ws://", 1), Token: testToken,
					Hello: Hello{SchemaVersion: SchemaVersion, AgentID: "agent-1", ProcessGeneration: "process-1"}, Authenticator: &fakeAuthenticator{},
					ReaderReadback: executor, Health: func() TopologySnapshot {
						return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}}
					}, OperationTimeout: time.Second}).Run(ctx)
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("Agent failed to stop")
				}
			}()
			deadline := time.Now().Add(2 * time.Second)
			for {
				if _, found := server.Status("agent-1"); found {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("Agent did not connect")
				}
				time.Sleep(time.Millisecond)
			}
			request := validReaderReadbackRequest()
			request.Scope = ReaderReadbackSIMIdentity
			result, err := server.ReadReader(context.Background(), "agent-1", "process-1", request)
			if capable {
				if err != nil || result.ValidateFor(request) != nil || calls.Load() != 1 {
					t.Fatalf("negotiated read result=%+v err=%v", result, err)
				}
			} else if !errors.Is(err, ErrReaderSIMIdentityUnsupported) || calls.Load() != 0 {
				t.Fatalf("SIM_SCOPE_DISPATCH: legacy received scoped frame: calls=%d err=%v", calls.Load(), err)
			}
			request.Scope = ""
			result, err = server.ReadReader(context.Background(), "agent-1", "process-1", request)
			if err != nil || result.State != "applied" || result.Scope != "" {
				t.Fatalf("legacy full read/connection broken: %+v %v", result, err)
			}
		})
	}
}

func TestReaderSIMIdentitySyntheticFailuresEchoScope(t *testing.T) {
	request := validReaderReadbackRequest()
	request.Scope = ReaderReadbackSIMIdentity
	for _, executor := range []ReaderReadbackExecutor{nil, readerReadbackFunc(func(_ context.Context, req ReaderReadbackRequest) ReaderReadbackResponse {
		result := validReaderReadbackResponse(req)
		result.Scope = ""
		return result
	})} {
		result := (Client{ReaderReadback: executor}).execute(context.Background(), envelope{Kind: kindReaderReadbackRequest, ReaderReadbackRequest: &request}).ReaderReadbackResult
		if result.ValidateFor(request) != nil || result.State == "applied" {
			t.Fatalf("invalid synthetic result=%+v", result)
		}
	}
}

func TestReaderReadbackResponseRequiresExactIdentity(t *testing.T) {
	request := validReaderReadbackRequest()
	response := validReaderReadbackResponse(request)
	response.CardID = "8944000000000000002"
	if err := response.ValidateFor(request); err == nil {
		t.Fatal("identity mismatch was accepted")
	}
}

func TestReaderReadbackResponseRequiresFailureCode(t *testing.T) {
	request := validReaderReadbackRequest()
	response := validReaderReadbackResponse(request)
	response.State = "unknown"
	response.Reader = nil
	if err := response.ValidateFor(request); err == nil {
		t.Fatal("unknown response without error code was accepted")
	}
}

func TestReaderReadbackEnvelopeRejectsMixedProvision(t *testing.T) {
	request := validReaderReadbackRequest()
	message := envelope{
		Kind: kindReaderReadbackRequest, RequestID: "wire-request-1",
		ReaderReadbackRequest: &request,
		ProvisionRequest:      &ProvisionRequest{ProvisionCommand: ProvisionCommand{}},
	}
	if err := message.validate(); err == nil {
		t.Fatal("mixed reader readback and provision envelope was accepted")
	}
}

func TestReadReaderRejectsAgentWithoutNegotiatedCapability(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := validReaderReadbackRequest()
	server.agents["agent-1"] = &serverConnection{hello: Hello{
		SchemaVersion: SchemaVersion, AgentID: "agent-1", ProcessGeneration: request.ProcessGeneration,
	}}
	if _, err := server.ReadReader(context.Background(), "agent-1", request.ProcessGeneration, request); !errors.Is(err, ErrReaderReadbackUnsupported) {
		t.Fatalf("ReadReader() error=%v, want %v", err, ErrReaderReadbackUnsupported)
	}
}

func TestReaderReadbackUsesNegotiatedAgentWSS(t *testing.T) {
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
			Authenticator: &fakeAuthenticator{}, ReaderReadback: fakeReaderReadbackExecutor{}, Health: func() TopologySnapshot {
				return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}}
			},
			HealthEvery: time.Second, OperationTimeout: time.Second,
		}).Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if status, found := server.Status("agent-1"); found && len(status.Capabilities) == 1 && status.Capabilities[0] == readerReadbackFeature {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reader readback Agent did not connect with negotiated capability")
		}
		time.Sleep(time.Millisecond)
	}
	request := validReaderReadbackRequest()
	result, err := server.ReadReader(context.Background(), "agent-1", "process-1", request)
	if err != nil || result.State != "applied" {
		var clientErr error
		select {
		case clientErr = <-done:
		default:
		}
		t.Fatalf("ReadReader() result=%+v error=%v client_error=%v", result, err, clientErr)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reader readback Agent did not stop")
	}
}
