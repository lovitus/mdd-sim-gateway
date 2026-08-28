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

func TestAgentHealthSendsFullTopologyThenLightweightHeartbeatsAndChanges(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	var topologyMu sync.RWMutex
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
		ReaderName: "reader-a", IdentityState: CardAbsent,
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL:   strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent",
			Token: testToken, Hello: Hello{SchemaVersion: 1, AgentID: "health-agent", ProcessGeneration: "health-process"},
			Authenticator: &fakeAuthenticator{}, OperationTimeout: time.Second, HealthEvery: 10 * time.Millisecond,
			Health: func() TopologySnapshot {
				topologyMu.RLock()
				defer topologyMu.RUnlock()
				return TopologySnapshot{
					ReaderCondition: topology.ReaderCondition, ReaderDetail: topology.ReaderDetail,
					Readers: append([]ReaderFact(nil), topology.Readers...),
				}
			},
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()

	first := waitForHealth(t, server, "health-agent", func(status ConnectionStatus) bool {
		return status.Topology != nil && len(status.Topology.Readers) == 1
	})
	firstRevision := first.TopologyRevision
	second := waitForHealth(t, server, "health-agent", func(status ConnectionStatus) bool {
		return status.LastReport.After(first.LastReport)
	})
	if second.TopologyRevision != firstRevision {
		t.Fatal("unchanged heartbeat replaced the topology revision")
	}
	second.Topology.Readers[0].ReaderName = "mutated"
	stored, _ := server.Status("health-agent")
	if stored.Topology.Readers[0].ReaderName != "reader-a" {
		t.Fatal("Status returned mutable server topology storage")
	}

	topologyMu.Lock()
	topology = TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
		ReaderName: "reader-a", CardPresent: true, SessionGeneration: "session-a",
		CardID: "89440001", IdentityState: CardIdentified,
	}}}
	topologyMu.Unlock()
	changed := waitForHealth(t, server, "health-agent", func(status ConnectionStatus) bool {
		return status.TopologyRevision != firstRevision
	})
	if changed.Topology == nil || changed.Topology.Readers[0].CardID != "89440001" {
		t.Fatalf("changed topology=%+v", changed.Topology)
	}
}

func TestTopologyRevisionRejectsAmbiguousOrUnsortedFacts(t *testing.T) {
	valid := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{
		{ReaderName: "reader-a", IdentityState: CardAbsent},
		{ReaderName: "reader-b", IdentityState: CardAbsent},
	}}
	revision, err := valid.Revision()
	if err != nil || len(revision) != 64 {
		t.Fatalf("revision=%q error=%v", revision, err)
	}
	invalid := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{
		{ReaderName: "reader-b", IdentityState: CardAbsent},
		{ReaderName: "reader-a", IdentityState: CardAbsent},
	}}
	if _, err := invalid.Revision(); err == nil {
		t.Fatal("unsorted topology was accepted")
	}
}

func TestTopologyValidatesAndDeepCopiesEUICCProfiles(t *testing.T) {
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
		ReaderName: "reader", CardPresent: true, SessionGeneration: "session",
		IdentityState: CardIdentified, EUICC: &EUICCFact{
			EID: "89049032000000000000000000000001", ProfilesAvailable: true,
			Profiles: []EUICCProfileFact{{ICCID: "8944000000000000001", State: EUICCProfileEnabled}},
		},
	}}}
	if err := topology.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := NormalizeTopology(topology)
	if copy.Readers[0].EUICC.Profiles == nil {
		t.Fatal("known blank eUICC profile list was normalized to null")
	}
	copy.Readers[0].EUICC.Profiles[0].ICCID = "1"
	if topology.Readers[0].EUICC.Profiles[0].ICCID != "8944000000000000001" {
		t.Fatal("NormalizeTopology retained mutable eUICC profile storage")
	}
	topology.Readers[0].EUICC.ProfilesAvailable = false
	if err := topology.Validate(); err == nil {
		t.Fatal("profiles present while unavailable were accepted")
	}
}

func TestServerRequiresFullFirstHealthAndMonotonicHeartbeats(t *testing.T) {
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}}
	revision, err := topology.Revision()
	if err != nil {
		t.Fatal(err)
	}
	connection := &serverConnection{}
	if err := connection.applyHealth(HealthReport{
		SchemaVersion: 1, Sequence: 1, TopologyRevision: revision,
	}); err == nil {
		t.Fatal("first heartbeat without topology was accepted")
	}
	if err := connection.applyHealth(HealthReport{
		SchemaVersion: 1, Sequence: 1, TopologyRevision: revision, Topology: &topology,
	}); err != nil {
		t.Fatal(err)
	}
	if err := connection.applyHealth(HealthReport{
		SchemaVersion: 1, Sequence: 1, TopologyRevision: revision,
	}); err == nil {
		t.Fatal("replayed health sequence was accepted")
	}
	if err := connection.applyHealth(HealthReport{
		SchemaVersion: 1, Sequence: 2, TopologyRevision: strings.Repeat("0", 64),
	}); err == nil {
		t.Fatal("heartbeat with a mismatched topology revision was accepted")
	}
}

func waitForHealth(t *testing.T, server *Server, agentID string, ready func(ConnectionStatus) bool) ConnectionStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, found := server.Status(agentID); found && ready(status) {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Agent %s health did not reach the expected state", agentID)
	return ConnectionStatus{}
}
