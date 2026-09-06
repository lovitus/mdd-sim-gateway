package agentlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestModemDialRequiresTypedLeaseAndDigitsOnlyNumber(t *testing.T) {
	base := ModemCommand{
		OperationID: "dial-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: ModemCallDial, LeaseID: "lease-1", Number: "+15550100123",
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, number := range []string{"+", "123;ATH", "0044 800"} {
		invalid := base
		invalid.Number = number
		if err := invalid.Validate(); err == nil {
			t.Fatalf("unsafe number %q was accepted", number)
		}
	}
}

func TestSMSRequestAcceptsOnlyTypedOptionalSessionFence(t *testing.T) {
	request := ModemRequest{OperationID: "sms-1", AttachmentID: "attachment-1",
		EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "sim-session-1", Action: ModemSMSSend, Number: "+15550100124", Body: "hello"}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	request.SIMSessionGeneration = "bad session"
	if err := request.Validate(); err == nil {
		t.Fatal("invalid SMS session fence was accepted")
	}
}

func TestIncomingCallActionsRequireExactFenceAndTypedTerminalResult(t *testing.T) {
	request := ModemRequest{OperationID: "incoming-reject-1", AttachmentID: "attachment-1",
		EquipmentID: "862547055201716", CardID: "8985200000000000001", Action: ModemCallReject,
		IncomingEventID: "incoming-1", SIMSessionGeneration: "session-1", NativeCallIndex: 4,
		CallOccurrence: 2, Number: "+44123"}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	response := ModemResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
		Call: &ModemCallResult{State: "idle", ObservedAt: time.Now(), Authoritative: true,
			TerminalConfirmed: true, Strategy: "incoming_chup"}}
	if err := response.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
	unsafe := request
	unsafe.IncomingEventID = ""
	if err := unsafe.Validate(); err == nil {
		t.Fatal("incoming reject without persistent event identity was accepted")
	}
	response.Call.Strategy = "chup"
	if err := response.ValidateFor(request); err == nil {
		t.Fatal("generic hangup result was accepted as incoming-only reject")
	}
}

type fakeAuthenticator struct {
	mu       sync.Mutex
	requests []AKARequest
	failure  *RemoteError
	wait     <-chan struct{}
}

type fakeModemExecutor struct {
	mu       sync.Mutex
	requests []ModemRequest
}

type fakeMediaExecutor struct {
	mu       sync.Mutex
	requests []ModemMediaRequest
}

type fakeDataExecutor struct {
	mu       sync.Mutex
	requests []ModemDataRequest
}

type fakePolicyExecutor struct{}

func (fakePolicyExecutor) ExecuteModemPolicy(_ context.Context, request ModemPolicyRequest) ModemPolicyResponse {
	response := ModemPolicyResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
		SIMSessionGeneration: request.SIMSessionGeneration,
		Policy: &ModemPolicyFact{SchemaVersion: 1, EquipmentID: request.EquipmentID, CardID: request.CardID,
			ProfileMode: "agent", State: "ready", Code: "policy_ready"}}
	if request.Action == ModemPolicyPrepareSIMAPDU {
		ready := true
		response.SIMAPDUReady = &ready
	}
	return response
}

type fakeModemEventSink struct{ events chan ModemEvent }

func (sink *fakeModemEventSink) AcceptModemEvent(_ context.Context, _ AgentEventContext, event ModemEvent) ModemEventDisposition {
	sink.events <- event
	return ModemEventDisposition{Accepted: true}
}

type fakeModemEventSource struct {
	mu    sync.Mutex
	event *ModemEvent
	wake  chan struct{}
	acked chan string
}

func (source *fakeModemEventSource) PendingModemEvents(time.Time, int) ([]ModemEvent, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.event == nil {
		return []ModemEvent{}, nil
	}
	return []ModemEvent{*source.event}, nil
}

func (source *fakeModemEventSource) AckModemEvent(eventID string) error {
	source.mu.Lock()
	source.event = nil
	source.mu.Unlock()
	source.acked <- eventID
	return nil
}

func (source *fakeModemEventSource) RejectModemEvent(eventID, _ string) error {
	return source.AckModemEvent(eventID)
}

func (source *fakeModemEventSource) ModemEventWake() <-chan struct{} { return source.wake }

func (fake *fakeDataExecutor) ExecuteModemData(_ context.Context, request ModemDataRequest) ModemDataResponse {
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	fake.mu.Unlock()
	state, profile := "ready", "mdd-profile"
	if request.Action == ModemDataOpen {
		state, profile = "open", ""
	} else if request.Action == ModemDataStop {
		state, profile = "stopped", ""
	}
	return ModemDataResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
		SIMSessionGeneration: request.SIMSessionGeneration, SessionID: request.SessionID,
		StreamID: request.StreamID, State: state, Profile: profile}
}

type fakeEUICCExecutor struct {
	mu       sync.Mutex
	requests []EUICCProfileRequest
}

type fakeEUICCDownloadExecutor struct {
	mu       sync.Mutex
	requests []EUICCDownloadRequest
}

type fakeEUICCDiscoveryExecutor struct {
	mu       sync.Mutex
	requests []EUICCDiscoveryRequest
}

type fakeEUICCNotificationExecutor struct {
	mu       sync.Mutex
	requests []EUICCNotificationRequest
}

func (fake *fakeEUICCNotificationExecutor) ExecuteEUICCNotification(_ context.Context,
	request EUICCNotificationRequest) EUICCNotificationResponse {
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	fake.mu.Unlock()
	result := EUICCNotificationResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration, EID: request.EID,
	}
	if request.Action == EUICCNotificationDeliver {
		result.Acknowledged, result.Removed = true, true
	} else if request.Action == EUICCNotificationRemove {
		result.Removed = true
	} else {
		result.Entries = []EUICCNotificationEntry{{SequenceNumber: 9, Event: "rpm", Address: "notify.example.com"}}
	}
	return result
}

func (fake *fakeEUICCDiscoveryExecutor) ExecuteEUICCDiscovery(_ context.Context,
	request EUICCDiscoveryRequest) EUICCDiscoveryResponse {
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	fake.mu.Unlock()
	return EUICCDiscoveryResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration, EID: request.EID,
		SMDS: "lpa.ds.gsma.com", Entries: []EUICCDiscoveryEntry{{EventID: "event-1", RSPServerAddress: "rsp.example.com"}},
	}
}

func (fake *fakeEUICCDownloadExecutor) ExecuteEUICCDownload(_ context.Context,
	request EUICCDownloadRequest) EUICCDownloadResponse {
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	fake.mu.Unlock()
	now := time.Now().UTC()
	return EUICCDownloadResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
		EID: request.EID, Action: request.Action,
		Job: &EUICCDownloadJob{
			State: EUICCDownloadQueued, Stage: EUICCDownloadStageQueued,
			StartedAt: now, UpdatedAt: now,
		},
	}
}

func (fake *fakeEUICCExecutor) ExecuteEUICCProfile(_ context.Context, request EUICCProfileRequest) EUICCProfileResponse {
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	fake.mu.Unlock()
	return EUICCProfileResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
		EID: request.EID, ICCID: request.ICCID, Action: request.Action,
		Outcome: EUICCProfileRefreshPending, Changed: true,
	}
}

func (fake *fakeMediaExecutor) ExecuteModemMedia(_ context.Context, request ModemMediaRequest) ModemMediaResponse {
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	fake.mu.Unlock()
	state := "ready"
	if request.Action == ModemMediaStop {
		state = "stopped"
	}
	return ModemMediaResponse{
		OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
		SessionID: request.SessionID, State: state,
	}
}

func (fake *fakeModemExecutor) ExecuteModem(_ context.Context, request ModemRequest) ModemResponse {
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	fake.mu.Unlock()
	response := ModemResponse{
		OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
	}
	if request.Action == ModemCallRenew {
		response.Lease = &ModemLeaseResult{LeaseID: request.LeaseID, ExpiresAt: time.Now().Add(45 * time.Second)}
		return response
	}
	if request.Action == ModemSMSList {
		response.SMS = &ModemSMSResult{State: "listed", Messages: []ModemSMSMessage{{
			Index: 1, State: "received", Direction: "in", Peer: "+15550100123", Body: "hello",
			ObservedAt: time.Now(), Fingerprint: strings.Repeat("a", 64),
		}}}
		return response
	}
	if request.Action == ModemSMSSend {
		response.SMS = &ModemSMSResult{State: "submitted", References: []int{0}}
		return response
	}
	response.Call = &ModemCallResult{
		State: "active", Direction: "out", Number: "+15550100124",
		ObservedAt: time.Now(), Authoritative: true,
	}
	if request.Action == ModemCallAnswer {
		response.Call.Direction = "in"
	}
	if request.Action == ModemCallHangup {
		response.Call.State = "idle"
		response.Call.Direction = ""
		response.Call.Number = ""
		response.Call.TerminalConfirmed = true
		response.Call.Strategy = "chup"
	}
	if request.Action == ModemCallDial || request.Action == ModemCallAnswer {
		response.Lease = &ModemLeaseResult{LeaseID: request.LeaseID, ExpiresAt: time.Now().Add(45 * time.Second)}
	}
	return response
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

func TestSMSSubmitUsesOnlyItsDocumentedLongOperationWindow(t *testing.T) {
	client := Client{OperationTimeout: 30 * time.Second}
	if got := client.timeoutFor(envelope{Kind: kindModemRequest, ModemRequest: &ModemRequest{Action: ModemSMSSend}}); got != smsSubmitOperationTimeout {
		t.Fatalf("SMS timeout=%s", got)
	}
	if got := client.timeoutFor(envelope{Kind: kindModemRequest, ModemRequest: &ModemRequest{Action: ModemCallDial}}); got != 30*time.Second {
		t.Fatalf("call timeout=%s", got)
	}
	if got := client.timeoutFor(envelope{Kind: kindDiscoveryRequest, DiscoveryRequest: &EUICCDiscoveryRequest{}}); got != euiccDiscoveryOperationTimeout {
		t.Fatalf("discovery timeout=%s", got)
	}
	if got := client.timeoutFor(envelope{Kind: kindProvisionRequest, ProvisionRequest: &ProvisionRequest{}}); got != provisionOperationTimeout {
		t.Fatalf("provision timeout=%s", got)
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
	clientStopped := false
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
		if clientStopped {
			return
		}
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
	server.DisconnectAgent("another-agent")
	if _, connected := server.Status("agent-1"); !connected {
		t.Fatal("unrelated credential invalidation disconnected the Agent")
	}
	server.DisconnectAgent("agent-1")
	select {
	case <-clientDone:
		clientStopped = true
	case <-time.After(2 * time.Second):
		t.Fatal("credential invalidation did not close the Agent connection")
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		if _, connected := server.Status("agent-1"); !connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("invalidated Agent remained in the connection directory")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAuthenticatedUpgradeNegotiatesDurableModemEvents(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	sink := &fakeModemEventSink{events: make(chan ModemEvent, 1)}
	if err := server.SetModemEventSink(sink); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	event := ModemEvent{SchemaVersion: ModemEventSchemaVersion, EventID: "cellular-sms-event-1",
		Kind: ModemEventKindSMS, AttachmentID: "attachment-1", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", SIMSessionGeneration: "session-1", ObservedAt: time.Now(),
		SMS: &ModemEventSMS{Index: 1, StorageIndices: []int{1}, Fingerprint: strings.Repeat("a", 64), State: "received", Direction: "in",
			Peer: "+44123", Body: "hello"}}
	source := &fakeModemEventSource{event: &event, wake: make(chan struct{}, 1), acked: make(chan string, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/v1/agent/connect",
			Token: testToken, Hello: Hello{SchemaVersion: SchemaVersion, AgentID: "agent-1", ProcessGeneration: "process-1"},
			Authenticator: &fakeAuthenticator{}, Events: source, OperationTimeout: time.Second}).Run(ctx)
	}()
	select {
	case got := <-sink.events:
		if got.EventID != event.EventID {
			t.Fatalf("event=%+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("negotiated modem event was not delivered")
	}
	select {
	case acked := <-source.acked:
		if acked != event.EventID {
			t.Fatalf("acked=%q", acked)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Core commit acknowledgement was not applied")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event client did not stop")
	}
}

func TestMissingUpgradeFeatureKeepsOutboxWithoutSendingUnknownEnvelope(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	event := ModemEvent{SchemaVersion: 1, EventID: "cellular-sms-event-rollback", Kind: ModemEventKindSMS,
		AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "session-1", ObservedAt: time.Now(),
		SMS: &ModemEventSMS{Index: 1, StorageIndices: []int{1}, Fingerprint: strings.Repeat("b", 64), State: "received", Direction: "in",
			Peer: "+44123", Body: "hello"}}
	source := &fakeModemEventSource{event: &event, wake: make(chan struct{}, 1), acked: make(chan string, 1)}
	connected := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/v1/agent/connect", Token: testToken,
			Hello:         Hello{SchemaVersion: SchemaVersion, AgentID: "agent-1", ProcessGeneration: "process-1"},
			Authenticator: &fakeAuthenticator{}, Events: source, OperationTimeout: time.Second,
			Connected: func() { close(connected) }}).Run(ctx)
	}()
	select {
	case <-connected:
	case err := <-done:
		t.Fatalf("rollback-compatible Agent stopped before connect: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("rollback-compatible Agent did not connect")
	}
	time.Sleep(100 * time.Millisecond)
	source.mu.Lock()
	retained := source.event != nil
	source.mu.Unlock()
	if !retained {
		t.Fatal("event outbox was consumed without negotiated feature")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("rollback-compatible Agent did not stop")
	}
}

func TestMissingPolicyUpgradeFeatureStripsPolicyFromLegacyHealthWire(t *testing.T) {
	received := make(chan TopologySnapshot, 1)
	handlerFailure := make(chan error, 1)
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			handlerFailure <- err
			return
		}
		defer socket.CloseNow()
		first, err := readEnvelope(request.Context(), socket)
		if err != nil || first.Kind != kindHello {
			handlerFailure <- errors.Join(err, errors.New("legacy server did not receive hello"))
			return
		}
		if writeEnvelope(request.Context(), socket, envelope{Kind: kindHelloAck}) != nil {
			handlerFailure <- errors.New("legacy server could not acknowledge hello")
			return
		}
		report, err := readEnvelope(request.Context(), socket)
		if err == nil && report.Kind == kindHealth && report.Health != nil && report.Health.Topology != nil {
			received <- *report.Health.Topology
		} else {
			handlerFailure <- errors.Join(err, errors.New("legacy server did not receive valid health"))
			return
		}
		<-request.Context().Done()
	})
	oldCore := httptest.NewServer(handler)
	defer oldCore.Close()
	policy := &ModemPolicyFact{SchemaVersion: 1, EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Revision: 1, Persisted: true, ProfileMode: "agent", State: "ready", Code: "policy_ready",
		Desired: ModemPolicyDesired{CellularEnabled: true}}
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}, ModemCondition: ModemReady,
		Host: &AgentHostFact{SchemaVersion: 1, Platform: "macos", Architecture: "arm64", BuildVersion: "revision-1",
			HostMode: "gui", Manager: "gui", SessionScope: "user", ConfigState: "ok", TokenConfigured: true,
			Storage: AgentStorageFact{State: "ok", TotalBytes: 1000, FreeBytes: 500, UsedPercent: 50}},
		Modems: []ModemFact{{AttachmentID: "attachment-a", EquipmentID: policy.EquipmentID, Condition: "ready",
			Capabilities: ModemCapabilities{CellularData: true}, AT: ModemATControlFact{State: "ready", Port: "COM16", SIMAPDUOnDemand: true},
			SIM:     ModemSIMFact{State: "ready", SessionGeneration: "session-a", ICCID: policy.CardID},
			Network: ModemNetworkFact{Registration: "home", SoftwareRadio: "on", HardwareRadio: "on", Data: "disconnected", DataGuard: "protected"},
			Policy:  policy}}}
	if err := topology.Validate(); err != nil {
		t.Fatalf("policy topology fixture is invalid: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{URL: strings.Replace(oldCore.URL, "http://", "ws://", 1) + "/v1/agent/ws", Token: testToken,
			Hello:         Hello{SchemaVersion: 1, AgentID: "rolling-agent", ProcessGeneration: "process-a"},
			Authenticator: &fakeAuthenticator{}, Policies: fakePolicyExecutor{},
			HostHealth: true,
			Health:     func() TopologySnapshot { return topology }, HealthEvery: 10 * time.Millisecond,
			OperationTimeout: time.Second}).Run(ctx)
	}()
	select {
	case wire := <-received:
		if wire.Host != nil || len(wire.Modems) != 1 || wire.Modems[0].Policy != nil || wire.Modems[0].AT.SIMAPDUOnDemand {
			t.Fatalf("legacy Core received additive policy field: %+v", wire)
		}
	case err := <-done:
		select {
		case serverErr := <-handlerFailure:
			t.Fatalf("rolling Agent stopped before health report: %v; legacy server: %v", err, serverErr)
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("rolling Agent stopped before health report: %v", err)
		}
	case err := <-handlerFailure:
		t.Fatalf("legacy server rejected rolling health: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("rolling Agent did not report legacy-compatible topology")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("rolling Agent did not stop")
	}
}

func TestRenewableDataRouteRequiresNegotiatedCapabilityWithoutBlockingLegacyManualRoute(t *testing.T) {
	now := time.Now().UTC()
	topology := &TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}, ModemCondition: ModemReady,
		Modems: []ModemFact{{AttachmentID: "attachment-a", EquipmentID: "862547055201716", Condition: "ready",
			Capabilities: ModemCapabilities{CellularData: true}, AT: ModemATControlFact{State: "ready", Port: "COM16"},
			SIM:     ModemSIMFact{State: "ready", SessionGeneration: "session-a", ICCID: "8985200000000000001"},
			Network: ModemNetworkFact{Registration: "home", SoftwareRadio: "on", HardwareRadio: "on", Data: "disconnected", DataGuard: "protected"}}}}
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	connection := &serverConnection{hello: Hello{SchemaVersion: 1, AgentID: "legacy-agent", ProcessGeneration: "process-a"},
		connectedAt: now, lastReport: now, topology: topology}
	connection.lastSeen.Store(now.UnixNano())
	server.agents["legacy-agent"] = connection
	manual, err := server.ResolveModemDataTargetForCard("8985200000000000001")
	if err != nil {
		t.Fatalf("legacy manual data route was rejected: %v", err)
	}
	if manual.SIMSessionGeneration != "" {
		t.Fatalf("legacy manual route changed its wire fence: %+v", manual)
	}
	if _, err := server.ResolveRenewableModemDataTargetForCard("8985200000000000001"); !errors.Is(err, ErrModemOffline) {
		t.Fatalf("legacy Agent accepted renewable egress route: %v", err)
	}
	connection.capabilities = []string{modemDataRenewFeature}
	renewable, err := server.ResolveRenewableModemDataTargetForCard("8985200000000000001")
	if err != nil {
		t.Fatalf("negotiated renewable route failed: %v", err)
	}
	if renewable.SIMSessionGeneration != "session-a" {
		t.Fatalf("renewable route omitted exact SIM generation: %+v", renewable)
	}
}

func TestOnDemandSIMAPDUCommandRequiresNegotiatedCapability(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	server.agents["legacy-agent"] = &serverConnection{
		hello: Hello{SchemaVersion: 1, AgentID: "legacy-agent", ProcessGeneration: "process-a"},
	}
	request := ModemPolicyRequest{
		OperationID: "prepare-apdu", AttachmentID: "attachment-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", SIMSessionGeneration: "session-a",
		Action: ModemPolicyPrepareSIMAPDU,
	}
	if _, err := server.ExecuteModemPolicy(context.Background(), "legacy-agent", "process-a", request); err == nil ||
		!strings.Contains(err.Error(), "does not support") {
		t.Fatalf("legacy prepare error=%v", err)
	}
}

func TestOnDemandSIMAPDUTopologyRequiresNegotiatedCapability(t *testing.T) {
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}, ModemCondition: ModemReady, Modems: []ModemFact{{
		AttachmentID: "attachment-a", EquipmentID: "862547055201716", Condition: "ready",
		AT:      ModemATControlFact{State: "ready", Port: "COM16", SIMAPDUOnDemand: true},
		SIM:     ModemSIMFact{State: "ready", ICCID: "8985200000000000001", SessionGeneration: "session-a"},
		Network: ModemNetworkFact{Registration: "home", SoftwareRadio: "on", HardwareRadio: "on", Data: "disconnected"},
	}}}
	revision, err := topology.Revision()
	if err != nil {
		t.Fatal(err)
	}
	connection := &serverConnection{}
	if err := connection.applyHealth(HealthReport{SchemaVersion: 1, Sequence: 1,
		TopologyRevision: revision, Topology: &topology}); err == nil || !strings.Contains(err.Error(), "without negotiation") {
		t.Fatalf("unnegotiated topology error=%v", err)
	}
	connection.capabilities = []string{modemPolicyFeature, modemSIMAPDUPrepareFeature}
	if err := connection.applyHealth(HealthReport{SchemaVersion: 1, Sequence: 1,
		TopologyRevision: revision, Topology: &topology}); err != nil {
		t.Fatalf("negotiated topology error=%v", err)
	}
}

func TestLegacyManualDataResponseOmitsRenewalOnlyFields(t *testing.T) {
	request := ModemDataRequest{OperationID: "prepare-legacy", AttachmentID: "attachment-a",
		EquipmentID: "862547055201716", CardID: "8985200000000000001", Action: ModemDataPrepare,
		SessionID: "session-a", ExpiresAt: time.Now().UTC().Add(time.Minute), MaxBytes: 1024}
	response := ModemDataResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID, SessionID: request.SessionID,
		State: "ready", Profile: "profile-a"}
	if err := response.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("expires_at")) || bytes.Contains(payload, []byte("purpose")) {
		t.Fatalf("legacy manual response contains renewable fields: %s", payload)
	}
}

func TestModemOperationUsesExistingAgentWSSAndExactTopologyFence(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	topology := TopologySnapshot{
		ReaderCondition: ReaderReady, Readers: []ReaderFact{}, ModemCondition: ModemReady,
		Modems: []ModemFact{{
			AttachmentID: "mbn-attachment-1", EquipmentID: "862547055201716", Condition: "ready",
			Capabilities: ModemCapabilities{CellularData: true},
			AT:           ModemATControlFact{State: "ready", Port: "COM16", CallSignalling: true, SMS: true},
			SIM:          ModemSIMFact{State: "ready", SessionGeneration: "sim-session-1", ICCID: "8985200000000000001"},
			Network: ModemNetworkFact{
				Registration: "roaming", SoftwareRadio: "on", HardwareRadio: "on", Data: "connected", DataGuard: "protected",
			},
		}},
	}
	executor := &fakeModemExecutor{}
	mediaExecutor := &fakeMediaExecutor{}
	dataExecutor := &fakeDataExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL:   strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent",
			Token: testToken, Hello: Hello{SchemaVersion: 1, AgentID: "modem-agent", ProcessGeneration: "process-1"},
			Authenticator: &fakeAuthenticator{}, Modems: executor, SMSSessionFencing: true,
			Media: mediaExecutor, Data: dataExecutor, OperationTimeout: time.Second,
			Health: func() TopologySnapshot { return topology }, HealthEvery: 10 * time.Millisecond,
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, found := server.Status("modem-agent")
		if found && status.Topology != nil && len(status.Topology.Modems) == 1 {
			if !featureEnabled(strings.Join(status.Capabilities, ","), modemSMSSessionFeature) {
				t.Fatalf("SMS session capability was not negotiated: %v", status.Capabilities)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("modem topology was not reported")
		}
		time.Sleep(time.Millisecond)
	}
	result, err := server.ExecuteModemCommand(context.Background(), ModemCommand{
		OperationID: "call-status-1", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: ModemCallStatus,
	})
	if err != nil || result.Call == nil || result.Call.State != "active" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	executor.mu.Lock()
	if len(executor.requests) != 1 || executor.requests[0].AttachmentID != "mbn-attachment-1" {
		t.Fatalf("requests=%+v", executor.requests)
	}
	executor.mu.Unlock()
	dial, err := server.ExecuteModemCommand(context.Background(), ModemCommand{
		OperationID: "call-dial-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: ModemCallDial, LeaseID: "paid-call-1", Number: "+15550100123",
	})
	if err != nil || dial.Lease == nil || dial.Call == nil {
		t.Fatalf("dial=%+v err=%v", dial, err)
	}
	renewal, err := server.ExecuteModemCommand(context.Background(), ModemCommand{
		OperationID: "call-renew-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: ModemCallRenew, LeaseID: "paid-call-1",
	})
	if err != nil || renewal.Lease == nil || renewal.Call != nil {
		t.Fatalf("renewal=%+v err=%v", renewal, err)
	}
	messages, err := server.ExecuteModemCommand(context.Background(), ModemCommand{
		OperationID: "sms-list-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: ModemSMSList,
	})
	if err != nil || messages.SMS == nil || len(messages.SMS.Messages) != 1 || messages.SMS.Messages[0].Body != "hello" {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	submitted, err := server.ExecuteModemCommand(context.Background(), ModemCommand{
		OperationID: "sms-send-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: ModemSMSSend, Number: "+15550100124", Body: "hello 世界",
	})
	if err != nil || submitted.SMS == nil || len(submitted.SMS.References) != 1 || submitted.SMS.References[0] != 0 {
		t.Fatalf("submitted=%+v err=%v", submitted, err)
	}
	executor.mu.Lock()
	if len(executor.requests) != 5 || executor.requests[1].Number != "+15550100123" ||
		executor.requests[2].LeaseID != "paid-call-1" || executor.requests[3].SIMSessionGeneration != "sim-session-1" ||
		executor.requests[4].SIMSessionGeneration != "sim-session-1" || executor.requests[4].Body != "hello 世界" {
		t.Fatalf("requests=%+v", executor.requests)
	}
	executor.mu.Unlock()
	media, err := server.ExecuteModemMediaCommand(context.Background(), ModemMediaCommand{
		OperationID: "media-prepare-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: ModemMediaPrepare, SessionID: "media-session-1", MediaToken: testToken,
	})
	if err != nil || media.State != "ready" {
		t.Fatalf("media=%+v err=%v", media, err)
	}
	mediaExecutor.mu.Lock()
	if len(mediaExecutor.requests) != 1 || mediaExecutor.requests[0].AttachmentID != "mbn-attachment-1" ||
		mediaExecutor.requests[0].SessionID != "media-session-1" {
		t.Fatalf("media requests=%+v", mediaExecutor.requests)
	}
	mediaExecutor.mu.Unlock()
	expiresAt := time.Now().UTC().Add(time.Hour)
	data, err := server.ExecuteModemDataCommand(context.Background(), ModemDataCommand{
		OperationID: "data-prepare-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: ModemDataPrepare, SessionID: "data-session-1", ExpiresAt: expiresAt, MaxBytes: 1 << 20,
	})
	if err != nil || data.State != "ready" || data.Profile != "mdd-profile" {
		t.Fatalf("data=%+v err=%v", data, err)
	}
	dataExecutor.mu.Lock()
	if len(dataExecutor.requests) != 1 || dataExecutor.requests[0].AttachmentID != "mbn-attachment-1" ||
		dataExecutor.requests[0].SIMSessionGeneration != "sim-session-1" {
		t.Fatalf("data requests=%+v", dataExecutor.requests)
	}
	dataExecutor.mu.Unlock()
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

func TestEUICCProfileCommandRequiresCapabilityAndUsesExactInsertionFence(t *testing.T) {
	const (
		eid   = "89049032000000000000000000000001"
		iccid = "8944000000000000001"
	)
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	var topologyMu sync.RWMutex
	management := false
	topology := func() TopologySnapshot {
		topologyMu.RLock()
		defer topologyMu.RUnlock()
		return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
			ReaderName: "reader-a", CardPresent: true, SessionGeneration: "insertion-1",
			CardID: iccid, IdentityState: CardIdentified,
			EUICC: &EUICCFact{
				EID: eid, ProfilesAvailable: true, ProfileManagement: management,
				Profiles: []EUICCProfileFact{{ICCID: iccid, State: EUICCProfileDisabled}},
			},
		}}}
	}
	executor := &fakeEUICCExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent", Token: testToken,
			Hello:         Hello{SchemaVersion: 1, AgentID: "euicc-agent", ProcessGeneration: "process-1"},
			Authenticator: &fakeAuthenticator{}, EUICC: executor, OperationTimeout: time.Second,
			Health: topology, HealthEvery: 10 * time.Millisecond,
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	waitForAgentEUICC(t, server, "euicc-agent", false)
	command := EUICCProfileCommand{
		OperationID: "profile-enable-1", EID: eid, ICCID: iccid,
		Action: EUICCProfileEnable, ExpectedState: EUICCProfileDisabled,
	}
	if _, err := server.ExecuteEUICCProfileCommand(context.Background(), command); !errors.Is(err, ErrCardOffline) {
		t.Fatalf("legacy capability error=%v", err)
	}
	executor.mu.Lock()
	legacyRequests := len(executor.requests)
	executor.mu.Unlock()
	if legacyRequests != 0 {
		t.Fatalf("legacy Agent received %d unsupported requests", legacyRequests)
	}

	topologyMu.Lock()
	management = true
	topologyMu.Unlock()
	waitForAgentEUICC(t, server, "euicc-agent", true)
	result, err := server.ExecuteEUICCProfileCommand(context.Background(), command)
	if err != nil || result.Outcome != EUICCProfileRefreshPending || !result.Changed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.requests) != 1 || executor.requests[0].SessionGeneration != "insertion-1" ||
		executor.requests[0].EID != eid || executor.requests[0].ICCID != iccid {
		t.Fatalf("requests=%+v", executor.requests)
	}
}

func waitForAgentEUICC(t *testing.T, server *Server, agentID string, management bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, found := server.Status(agentID)
		if found && status.Topology != nil && len(status.Topology.Readers) == 1 &&
			status.Topology.Readers[0].EUICC != nil && status.Topology.Readers[0].EUICC.ProfileManagement == management {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Agent %s did not report profile_management=%t", agentID, management)
}

func TestEUICCDownloadRequiresCapabilityAndUsesExactInsertionFence(t *testing.T) {
	const eid = "89049032000000000000000000000001"
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	var topologyMu sync.RWMutex
	downloadCapable := false
	topology := func() TopologySnapshot {
		topologyMu.RLock()
		defer topologyMu.RUnlock()
		return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
			ReaderName: "reader-download", CardPresent: true, SessionGeneration: "insertion-download-1",
			IdentityState: CardIdentified, EUICC: &EUICCFact{
				EID: eid, ProfilesAvailable: true, ProfileDownload: downloadCapable,
				Profiles: []EUICCProfileFact{},
			},
		}}}
	}
	executor := &fakeEUICCDownloadExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent", Token: testToken,
			Hello:         Hello{SchemaVersion: 1, AgentID: "download-agent", ProcessGeneration: "download-process-1"},
			Authenticator: &fakeAuthenticator{}, Downloads: executor, OperationTimeout: time.Second,
			Health: topology, HealthEvery: 10 * time.Millisecond,
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	waitForAgentDownloadCapability(t, server, "download-agent", false)
	command := EUICCDownloadCommand{
		OperationID: "download-1", EID: eid, Action: EUICCDownloadStart,
		ActivationCode: "LPA:1$example.com$matching-id", ConfirmationCode: "one-use",
		IMEI: "123456789012345",
	}
	if _, err := server.ExecuteEUICCDownloadCommand(context.Background(), command); !errors.Is(err, ErrCardOffline) {
		t.Fatalf("legacy capability error=%v", err)
	}
	topologyMu.Lock()
	downloadCapable = true
	topologyMu.Unlock()
	waitForAgentDownloadCapability(t, server, "download-agent", true)
	select {
	case runErr := <-done:
		t.Fatalf("download Agent disconnected before request: %v", runErr)
	default:
	}
	result, err := server.ExecuteEUICCDownloadCommand(context.Background(), command)
	if err != nil || result.Job == nil || result.Job.State != EUICCDownloadQueued {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.requests) != 1 || executor.requests[0].SessionGeneration != "insertion-download-1" ||
		executor.requests[0].EID != eid || executor.requests[0].ActivationCode != command.ActivationCode ||
		executor.requests[0].ConfirmationCode != command.ConfirmationCode {
		t.Fatalf("requests=%+v", executor.requests)
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte(command.ActivationCode)) || bytes.Contains(wire, []byte(command.ConfirmationCode)) {
		t.Fatalf("download response echoed one-use secrets: %s", wire)
	}
}

func TestEUICCDownloadJobRejectsInconsistentStateAndStage(t *testing.T) {
	now := time.Now().UTC()
	valid := EUICCDownloadJob{
		State: EUICCDownloadRunning, Stage: EUICCDownloadStageAuthenticateServer,
		StartedAt: now, UpdatedAt: now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []EUICCDownloadJob{
		{State: EUICCDownloadCompleted, Stage: EUICCDownloadStageInstall, StartedAt: now, UpdatedAt: now},
		{State: EUICCDownloadRunning, Stage: EUICCDownloadStageAuthenticateClient, Code: "failed", StartedAt: now, UpdatedAt: now},
		{State: EUICCDownloadCanceled, Stage: EUICCDownloadStageAuthenticateClient, StartedAt: now, UpdatedAt: now},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("inconsistent job was accepted: %+v", invalid)
		}
	}
}

func TestEUICCDiscoveryRequiresCapabilityAndUsesExactInsertionFence(t *testing.T) {
	const eid = "89049032000000000000000000000001"
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	var topologyMu sync.RWMutex
	capable := false
	topology := func() TopologySnapshot {
		topologyMu.RLock()
		defer topologyMu.RUnlock()
		return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
			ReaderName: "reader-discovery", CardPresent: true, SessionGeneration: "insertion-discovery-1",
			IdentityState: CardIdentified, EUICC: &EUICCFact{
				EID: eid, ProfilesAvailable: true, ProfileDiscovery: capable, Profiles: []EUICCProfileFact{},
			},
		}}}
	}
	executor := &fakeEUICCDiscoveryExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent", Token: testToken,
			Hello:         Hello{SchemaVersion: 1, AgentID: "discovery-agent", ProcessGeneration: "discovery-process-1"},
			Authenticator: &fakeAuthenticator{}, Discovery: executor, OperationTimeout: time.Second,
			Health: topology, HealthEvery: 10 * time.Millisecond,
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	waitForAgentDiscoveryCapability(t, server, "discovery-agent", false)
	command := EUICCDiscoveryCommand{OperationID: "discovery-1", EID: eid, IMEI: "123456789012345"}
	if _, err := server.ExecuteEUICCDiscoveryCommand(context.Background(), command); !errors.Is(err, ErrCardOffline) {
		t.Fatalf("legacy capability error=%v", err)
	}
	topologyMu.Lock()
	capable = true
	topologyMu.Unlock()
	waitForAgentDiscoveryCapability(t, server, "discovery-agent", true)
	result, err := server.ExecuteEUICCDiscoveryCommand(context.Background(), command)
	if err != nil || result.SMDS != "lpa.ds.gsma.com" || len(result.Entries) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.requests) != 1 || executor.requests[0].SessionGeneration != "insertion-discovery-1" ||
		executor.requests[0].EID != eid || executor.requests[0].IMEI != command.IMEI {
		t.Fatalf("requests=%+v", executor.requests)
	}
}

func waitForAgentDiscoveryCapability(t *testing.T, server *Server, agentID string, capable bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, found := server.Status(agentID)
		if found && status.Topology != nil && len(status.Topology.Readers) == 1 &&
			status.Topology.Readers[0].EUICC != nil && status.Topology.Readers[0].EUICC.ProfileDiscovery == capable {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Agent %s did not report profile_discovery=%t", agentID, capable)
}

func TestEUICCNotificationInventoryRequiresCapabilityAndUsesExactInsertionFence(t *testing.T) {
	const eid = "89049032000000000000000000000001"
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	var topologyMu sync.RWMutex
	capable := false
	topology := func() TopologySnapshot {
		topologyMu.RLock()
		defer topologyMu.RUnlock()
		return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
			ReaderName: "reader-notification", CardPresent: true, SessionGeneration: "insertion-notification-1",
			IdentityState: CardIdentified, EUICC: &EUICCFact{
				EID: eid, ProfilesAvailable: true, NotificationInventory: capable,
				NotificationDelivery: capable, NotificationRemoval: capable, Profiles: []EUICCProfileFact{},
			},
		}}}
	}
	executor := &fakeEUICCNotificationExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent", Token: testToken,
			Hello:         Hello{SchemaVersion: 1, AgentID: "notification-agent", ProcessGeneration: "notification-process-1"},
			Authenticator: &fakeAuthenticator{}, Notifications: executor, OperationTimeout: time.Second,
			Health: topology, HealthEvery: 10 * time.Millisecond,
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	waitForAgentNotificationCapability(t, server, "notification-agent", false)
	command := EUICCNotificationCommand{OperationID: "notification-1", EID: eid}
	if _, err := server.ExecuteEUICCNotificationCommand(context.Background(), command); !errors.Is(err, ErrCardOffline) {
		t.Fatalf("legacy capability error=%v", err)
	}
	topologyMu.Lock()
	capable = true
	topologyMu.Unlock()
	waitForAgentNotificationCapability(t, server, "notification-agent", true)
	result, err := server.ExecuteEUICCNotificationCommand(context.Background(), command)
	if err != nil || len(result.Entries) != 1 || result.Entries[0].Event != "rpm" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	executor.mu.Lock()
	if len(executor.requests) != 1 || executor.requests[0].SessionGeneration != "insertion-notification-1" ||
		executor.requests[0].EID != eid {
		executor.mu.Unlock()
		t.Fatalf("requests=%+v", executor.requests)
	}
	executor.mu.Unlock()
	expected := &EUICCNotificationEntry{SequenceNumber: 9, Event: "rpm", Address: "notify.example.com"}
	delivery, err := server.ExecuteEUICCNotificationCommand(context.Background(), EUICCNotificationCommand{
		OperationID: "notification-delivery-1", EID: eid, Action: EUICCNotificationDeliver, Expected: expected,
	})
	executor.mu.Lock()
	if err != nil || !delivery.Acknowledged || !delivery.Removed || len(executor.requests) != 2 ||
		executor.requests[1].Action != EUICCNotificationDeliver || executor.requests[1].Expected == expected ||
		executor.requests[1].Expected == nil || *executor.requests[1].Expected != *expected {
		executor.mu.Unlock()
		t.Fatalf("delivery=%+v requests=%+v err=%v", delivery, executor.requests, err)
	}
	executor.mu.Unlock()
	removal, err := server.ExecuteEUICCNotificationCommand(context.Background(), EUICCNotificationCommand{
		OperationID: "notification-removal-1", EID: eid, Action: EUICCNotificationRemove, Expected: expected,
	})
	executor.mu.Lock()
	if err != nil || removal.Acknowledged || !removal.Removed || len(executor.requests) != 3 ||
		executor.requests[2].Action != EUICCNotificationRemove || executor.requests[2].Expected == expected ||
		executor.requests[2].Expected == nil || *executor.requests[2].Expected != *expected {
		executor.mu.Unlock()
		t.Fatalf("removal=%+v requests=%+v err=%v", removal, executor.requests, err)
	}
	executor.mu.Unlock()
}

func waitForAgentNotificationCapability(t *testing.T, server *Server, agentID string, capable bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, found := server.Status(agentID)
		if found && status.Topology != nil && len(status.Topology.Readers) == 1 &&
			status.Topology.Readers[0].EUICC != nil &&
			status.Topology.Readers[0].EUICC.NotificationInventory == capable &&
			status.Topology.Readers[0].EUICC.NotificationDelivery == capable &&
			status.Topology.Readers[0].EUICC.NotificationRemoval == capable {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Agent %s did not report notification_inventory=%t", agentID, capable)
}

func waitForAgentDownloadCapability(t *testing.T, server *Server, agentID string, capable bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, found := server.Status(agentID)
		if found && status.Topology != nil && len(status.Topology.Readers) == 1 &&
			status.Topology.Readers[0].EUICC != nil && status.Topology.Readers[0].EUICC.ProfileDownload == capable {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Agent %s did not report profile_download=%t", agentID, capable)
}

func TestCardResolutionTracksHotplugAndRejectsDuplicateIdentity(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	type liveTopology struct {
		mu       sync.RWMutex
		snapshot TopologySnapshot
	}
	firstTopology := &liveTopology{snapshot: identifiedTopology("session-1", "8907")}
	firstAuth := &fakeAuthenticator{}
	firstContext, stopFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- (Client{
			URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent", Token: testToken,
			Hello:         Hello{SchemaVersion: 1, AgentID: "resolver-a", ProcessGeneration: "process-a"},
			Authenticator: firstAuth, OperationTimeout: time.Second, HealthEvery: 10 * time.Millisecond,
			Health: func() TopologySnapshot {
				firstTopology.mu.RLock()
				defer firstTopology.mu.RUnlock()
				return NormalizeTopology(firstTopology.snapshot)
			},
		}).Run(firstContext)
	}()
	defer func() {
		stopFirst()
		<-firstDone
	}()
	waitForAgentCard(t, server, "resolver-a", "session-1", "8907")

	challenge := AKAChallenge{OperationID: "resolve-1", CardID: "8907", Application: AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16)}
	response, err := server.AuthenticateCardAKA(context.Background(), challenge)
	if err != nil || response.SessionGeneration != "session-1" {
		t.Fatalf("initial resolved response=%+v err=%v", response, err)
	}
	resolved, err := server.ResolveCardRoute("8907")
	if err != nil || resolved.AgentID != "resolver-a" || resolved.ProcessGeneration != "process-a" ||
		resolved.SessionGeneration != "session-1" || resolved.Kind != "reader" {
		t.Fatalf("resolved route=%+v err=%v", resolved, err)
	}

	firstTopology.mu.Lock()
	firstTopology.snapshot = TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
		ReaderName: "reader-1", IdentityState: CardAbsent,
	}}}
	firstTopology.mu.Unlock()
	waitForAgentCard(t, server, "resolver-a", "", "")
	challenge.OperationID = "resolve-absent"
	if _, err := server.AuthenticateCardAKA(context.Background(), challenge); !errors.Is(err, ErrCardOffline) {
		t.Fatalf("absent card error=%v", err)
	}
	if _, err := server.ResolveCardRoute("8907"); !errors.Is(err, ErrCardOffline) {
		t.Fatalf("absent route error=%v", err)
	}

	firstTopology.mu.Lock()
	firstTopology.snapshot = identifiedTopology("session-2", "8907")
	firstTopology.mu.Unlock()
	waitForAgentCard(t, server, "resolver-a", "session-2", "8907")
	challenge.OperationID = "resolve-2"
	response, err = server.AuthenticateCardAKA(context.Background(), challenge)
	if err != nil || response.SessionGeneration != "session-2" {
		t.Fatalf("reinserted response=%+v err=%v", response, err)
	}

	secondContext, stopSecond := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- (Client{
			URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent", Token: testToken,
			Hello:         Hello{SchemaVersion: 1, AgentID: "resolver-b", ProcessGeneration: "process-b"},
			Authenticator: &fakeAuthenticator{}, OperationTimeout: time.Second, HealthEvery: 10 * time.Millisecond,
			Health: func() TopologySnapshot { return identifiedTopology("session-b", "8907") },
		}).Run(secondContext)
	}()
	defer func() {
		stopSecond()
		<-secondDone
	}()
	waitForAgentCard(t, server, "resolver-b", "session-b", "8907")
	challenge.OperationID = "resolve-ambiguous"
	if _, err := server.AuthenticateCardAKA(context.Background(), challenge); !errors.Is(err, ErrCardAmbiguous) {
		t.Fatalf("duplicate card error=%v", err)
	}
	if _, err := server.ResolveCardRoute("8907"); !errors.Is(err, ErrCardAmbiguous) {
		t.Fatalf("duplicate route error=%v", err)
	}
}

func waitForAgentCard(t *testing.T, server *Server, agentID, sessionGeneration, cardID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, found := server.Status(agentID)
		if found && status.Topology != nil && len(status.Topology.Readers) == 1 {
			reader := status.Topology.Readers[0]
			if reader.SessionGeneration == sessionGeneration && reader.CardID == cardID {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Agent %s did not report session=%s card=%s", agentID, sessionGeneration, cardID)
}

func TestAuthenticateCardAKAResolvesTypedModemSIMAndRejectsDuplicateSource(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	modem := ModemFact{
		AttachmentID: "mbn-a", EquipmentID: "862547055201716", Condition: "degraded", Detail: "signal fact unavailable",
		AT:      ModemATControlFact{State: "ready", Port: "COM16", SIMAPDU: true},
		SIM:     ModemSIMFact{State: "ready", SessionGeneration: "modem-session-1", ICCID: "8907"},
		Network: ModemNetworkFact{Registration: "roaming", SoftwareRadio: "on", HardwareRadio: "on", Data: "disconnected"},
	}
	var topologyMu sync.RWMutex
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}, ModemCondition: ModemReady, Modems: []ModemFact{modem}}
	authenticator := &fakeAuthenticator{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent", Token: testToken,
			Hello:         Hello{SchemaVersion: 1, AgentID: "modem-aka-agent", ProcessGeneration: "process-1"},
			Authenticator: authenticator, OperationTimeout: time.Second, HealthEvery: 10 * time.Millisecond,
			Health: func() TopologySnapshot {
				topologyMu.RLock()
				defer topologyMu.RUnlock()
				return NormalizeTopology(topology)
			},
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, found := server.Status("modem-aka-agent")
		if found && status.Topology != nil && len(status.Topology.Modems) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("modem SIM topology was not published")
		}
		time.Sleep(time.Millisecond)
	}
	challenge := AKAChallenge{OperationID: "modem-aka-1", CardID: "8907", Application: AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16)}
	response, err := server.AuthenticateCardAKA(context.Background(), challenge)
	if err != nil || response.SessionGeneration != "modem-session-1" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	authenticator.mu.Lock()
	request := authenticator.requests[len(authenticator.requests)-1]
	authenticator.mu.Unlock()
	if request.DeviceKind != AKADeviceModem || request.AttachmentID != "mbn-a" || request.EquipmentID != "862547055201716" {
		t.Fatalf("request=%+v", request)
	}

	topologyMu.Lock()
	topology.Readers = []ReaderFact{{ReaderName: "reader-a", CardPresent: true, SessionGeneration: "reader-session", CardID: "8907", IdentityState: CardIdentified}}
	topologyMu.Unlock()
	deadline = time.Now().Add(2 * time.Second)
	for {
		status, _ := server.Status("modem-aka-agent")
		if status.Topology != nil && len(status.Topology.Readers) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("duplicate reader topology was not published")
		}
		time.Sleep(time.Millisecond)
	}
	challenge.OperationID = "modem-aka-ambiguous"
	if _, err := server.AuthenticateCardAKA(context.Background(), challenge); !errors.Is(err, ErrCardAmbiguous) {
		t.Fatalf("duplicate source error=%v", err)
	}
}

func TestModemDataGuardMachineStateRequiresFailureDetail(t *testing.T) {
	base := TopologySnapshot{
		ReaderCondition: ReaderReady,
		ModemCondition:  ModemReady,
		Modems: []ModemFact{{
			AttachmentID: "mbn-a",
			Condition:    "degraded",
			Detail:       "data_guard: WFP unavailable",
			SIM:          ModemSIMFact{State: "unknown"},
			Network: ModemNetworkFact{
				Registration: "unknown", SoftwareRadio: "unknown", HardwareRadio: "unknown", Data: "unknown",
				DataGuard: "failed", DataGuardDetail: "WFP unavailable",
			},
		}},
	}
	if err := base.validateModems(); err != nil {
		t.Fatalf("valid failed guard state rejected: %v", err)
	}
	base.Modems[0].Network.DataGuardDetail = ""
	if err := base.validateModems(); err == nil {
		t.Fatal("failed guard state without detail accepted")
	}
	base.Modems[0].Network.DataGuard = "protected"
	base.Modems[0].Network.DataGuardDetail = "unexpected detail"
	if err := base.validateModems(); err == nil {
		t.Fatal("protected guard state with failure detail accepted")
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

func TestReaderAKARequestKeepsLegacyWireShape(t *testing.T) {
	challenge := AKAChallenge{OperationID: "aka-1", CardID: "8907", Application: AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16)}
	payload, err := json.Marshal(challenge.requestFor("reader-session"))
	if err != nil {
		t.Fatal(err)
	}
	for _, added := range []string{"device_kind", "attachment_id", "equipment_id"} {
		if bytes.Contains(payload, []byte(added)) {
			t.Fatalf("legacy reader request contains %s: %s", added, payload)
		}
	}
}

func TestAgentHealthSendsFullTopologyThenLightweightHeartbeatsAndChanges(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	var topologyMu sync.RWMutex
	signal := uint32(55)
	topology := TopologySnapshot{
		Host: &AgentHostFact{SchemaVersion: 1, Platform: "linux", Architecture: "amd64", BuildVersion: "revision-1",
			HostMode: "service", Manager: "systemd", SessionScope: "machine", ConfigState: "ok", TokenConfigured: true,
			ModemEnabled: true, Storage: AgentStorageFact{State: "ok", TotalBytes: 1000, FreeBytes: 500, UsedPercent: 50}},
		ReaderCondition: ReaderReady, Readers: []ReaderFact{{ReaderName: "reader-a", IdentityState: CardAbsent}},
		ModemCondition: ModemReady, Modems: []ModemFact{{
			AttachmentID: "mbn-a", Condition: "ready", SIM: ModemSIMFact{State: "ready", MSISDNs: []string{"+441"}},
			Network: ModemNetworkFact{Registration: "home", SignalPercent: &signal, SoftwareRadio: "on", HardwareRadio: "on", Data: "connected"},
		}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Client{
			URL:   strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/agent",
			Token: testToken, Hello: Hello{SchemaVersion: 1, AgentID: "health-agent", ProcessGeneration: "health-process"},
			Authenticator: &fakeAuthenticator{}, OperationTimeout: time.Second, HealthEvery: 10 * time.Millisecond,
			HostHealth: true,
			Health: func() TopologySnapshot {
				topologyMu.RLock()
				defer topologyMu.RUnlock()
				return NormalizeTopology(topology)
			},
		}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()

	first := waitForHealth(t, server, "health-agent", func(status ConnectionStatus) bool {
		return status.Topology != nil && status.Topology.Host != nil && len(status.Topology.Readers) == 1 && len(status.Topology.Modems) == 1
	})
	firstRevision := first.TopologyRevision
	second := waitForHealth(t, server, "health-agent", func(status ConnectionStatus) bool {
		return status.LastReport.After(first.LastReport)
	})
	if second.TopologyRevision != firstRevision {
		t.Fatal("unchanged heartbeat replaced the topology revision")
	}
	second.Topology.Readers[0].ReaderName = "mutated"
	second.Topology.Host.Platform = "windows"
	second.Topology.Modems[0].SIM.MSISDNs[0] = "+44999"
	*second.Topology.Modems[0].Network.SignalPercent = 1
	stored, _ := server.Status("health-agent")
	if stored.Topology.Host.Platform != "linux" || stored.Topology.Readers[0].ReaderName != "reader-a" || stored.Topology.Modems[0].SIM.MSISDNs[0] != "+441" ||
		*stored.Topology.Modems[0].Network.SignalPercent != 55 {
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
	copy := NormalizeTopology(topology)
	if err := copy.Validate(); err != nil {
		t.Fatal(err)
	}
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

func TestTopologyValidatesAndDeepCopiesReaderSIMIdentity(t *testing.T) {
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
		ReaderName: "reader", CardPresent: true, SessionGeneration: "session", CardID: "8944000000000000001",
		IdentityState: CardIdentified, SIM: &ReaderSIMFact{IdentityState: "ready",
			IMSI: "234100000000001", MCC: "234", MNC: "10", SMSC: "+447785016005"},
	}}}
	copy := NormalizeTopology(topology)
	if err := copy.Validate(); err != nil {
		t.Fatal(err)
	}
	copy.Readers[0].SIM.IMSI = "234100000000002"
	if topology.Readers[0].SIM.IMSI != "234100000000001" {
		t.Fatal("NormalizeTopology retained mutable reader SIM identity")
	}
	invalid := topology
	invalid.Readers = append([]ReaderFact(nil), topology.Readers...)
	bad := *topology.Readers[0].SIM
	bad.MNC = "20"
	invalid.Readers[0].SIM = &bad
	if err := invalid.Validate(); err == nil {
		t.Fatal("reader SIM identity inconsistent with IMSI was accepted")
	}
}

func TestTopologyValidatesAndDeepCopiesAgentHostHealth(t *testing.T) {
	host := &AgentHostFact{SchemaVersion: 1, Platform: "macos", Architecture: "arm64",
		BuildVersion: "revision-1", HostMode: "gui", Manager: "gui", SessionScope: "user",
		ConfigState: "ok", TokenConfigured: true, ModemEnabled: true,
		Storage: AgentStorageFact{State: "ok", TotalBytes: 1000, FreeBytes: 400, UsedPercent: 60}}
	topology := TopologySnapshot{Host: host, ReaderCondition: ReaderReady, Readers: []ReaderFact{},
		ModemCondition: ModemDisabled, Modems: []ModemFact{}}
	copy := NormalizeTopology(topology)
	if err := copy.Validate(); err != nil {
		t.Fatal(err)
	}
	copy.Host.Platform = "linux"
	if topology.Host.Platform != "macos" {
		t.Fatal("NormalizeTopology retained mutable host health")
	}
	bad := *host
	bad.Storage.State, bad.Storage.ErrorCode = "unknown", ""
	bad.Storage.TotalBytes, bad.Storage.FreeBytes, bad.Storage.UsedPercent = 0, 0, 0
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown storage without an error code was accepted")
	}
}

func TestTopologyValidatesDistinctSecureElementsAndRejectsMixedLegacyFacts(t *testing.T) {
	reader := ReaderFact{ReaderName: "reader", CardPresent: true, SessionGeneration: "session",
		IdentityState: CardIdentified, SecureElements: []EUICCSlotFact{
			{SlotID: "se0", Label: "SE1", EUICC: EUICCFact{EID: "89049032000000000000000000000001", ProfilesAvailable: true, Profiles: []EUICCProfileFact{}}},
			{SlotID: "se1", Label: "SE2", EUICC: EUICCFact{EID: "89049032000000000000000000000002", ProfilesAvailable: true, Profiles: []EUICCProfileFact{}}},
		}}
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{reader}}
	copy := NormalizeTopology(topology)
	if err := copy.Validate(); err != nil || len(ReaderEUICCs(copy.Readers[0])) != 2 {
		t.Fatalf("dual-SE topology=%+v error=%v", copy, err)
	}
	copy.Readers[0].SecureElements[0].EUICC.EID = "89049032000000000000000000000003"
	if topology.Readers[0].SecureElements[0].EUICC.EID != "89049032000000000000000000000001" {
		t.Fatal("NormalizeTopology retained mutable secure-element storage")
	}
	reader.EUICC = &EUICCFact{EID: "89049032000000000000000000000003", ProfilesAvailable: true, Profiles: []EUICCProfileFact{}}
	if err := (TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{reader}}).Validate(); err == nil {
		t.Fatal("mixed legacy and multi-SE facts were accepted")
	}
}

func TestTopologyModemFactsAreTypedSortedAndDeepCopied(t *testing.T) {
	signal := uint32(73)
	topology := TopologySnapshot{
		ReaderCondition: ReaderReady, Readers: []ReaderFact{}, ModemCondition: ModemReady,
		Modems: []ModemFact{
			{
				AttachmentID: "mbn-b", Condition: "ready",
				Capabilities: ModemCapabilities{CellularData: true, SMSReceive: true, SMSSend: true, MBNVoiceClass: "simultaneous_voice_data"},
				AT:           ModemATControlFact{State: "ready", Port: "COM16", CallSignalling: true, SMS: true},
				SIM:          ModemSIMFact{State: "ready", ICCID: "8944100000000000002", IMSI: "234100000000002", MSISDNs: []string{"+442"}},
				Network:      ModemNetworkFact{Registration: "roaming", SignalPercent: &signal, SoftwareRadio: "on", HardwareRadio: "on", Data: "connected"},
			},
			{
				AttachmentID: "mbn-a", Condition: "ready", SIM: ModemSIMFact{State: "absent"},
				AT:      ModemATControlFact{State: "unavailable", Detail: "no matching auxiliary AT control port was found"},
				Network: ModemNetworkFact{Registration: "unregistered", SoftwareRadio: "on", HardwareRadio: "on", Data: "disconnected"},
			},
		},
	}
	copy := NormalizeTopology(topology)
	if err := copy.Validate(); err != nil {
		t.Fatal(err)
	}
	if copy.Modems[0].AttachmentID != "mbn-a" || copy.Modems[1].AttachmentID != "mbn-b" {
		t.Fatalf("modems not sorted: %+v", copy.Modems)
	}
	topology.Modems[0].SIM.MSISDNs[0] = "+44999"
	*topology.Modems[0].Network.SignalPercent = 1
	if copy.Modems[1].SIM.MSISDNs[0] != "+442" || *copy.Modems[1].Network.SignalPercent != 73 {
		t.Fatal("NormalizeTopology retained mutable modem storage")
	}
	copy.Modems[1].LastContinuityIssue = "raw AT response"
	if err := copy.Validate(); err == nil {
		t.Fatal("untyped modem continuity issue was accepted")
	}
}

func TestTopologyKeepsLegacySchemaOnePCSCReportValid(t *testing.T) {
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}}
	if err := topology.Validate(); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(topology)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("modem_")) || bytes.Contains(payload, []byte("modems")) {
		t.Fatalf("legacy topology unexpectedly changed wire shape: %s", payload)
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

func TestServerSeparatesOlderWireRevisionFromCanonicalTopologyRevision(t *testing.T) {
	topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}}
	canonicalRevision, err := topology.Revision()
	if err != nil {
		t.Fatal(err)
	}
	olderWireRevision := strings.Repeat("1", 64)
	report := HealthReport{
		SchemaVersion: 1, Sequence: 1, TopologyRevision: olderWireRevision, Topology: &topology,
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("validated older topology wire shape was rejected: %v", err)
	}
	connection := &serverConnection{}
	if err := connection.applyHealth(report); err != nil {
		t.Fatal(err)
	}
	if connection.wireTopoRev != olderWireRevision || connection.topologyRev != canonicalRevision {
		t.Fatalf("wire revision=%q canonical revision=%q", connection.wireTopoRev, connection.topologyRev)
	}
	if err := connection.applyHealth(HealthReport{
		SchemaVersion: 1, Sequence: 2, TopologyRevision: olderWireRevision,
	}); err != nil {
		t.Fatalf("matching lightweight heartbeat was rejected: %v", err)
	}
	invalid := topology
	invalid.ReaderCondition = "invented"
	if err := (HealthReport{
		SchemaVersion: 1, Sequence: 3, TopologyRevision: olderWireRevision, Topology: &invalid,
	}).Validate(); err == nil {
		t.Fatal("invalid topology content was accepted")
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
