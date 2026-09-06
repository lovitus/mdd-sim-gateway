package agentlink

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeSIMPINExecutor struct{}

func (fakeSIMPINExecutor) ExecuteSIMPIN(_ context.Context, request SIMPINRequest) SIMPINResponse {
	response := SIMPINResponse{OperationID: request.OperationID, CardID: request.CardID,
		ReaderName: request.ReaderName, AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID,
		SIMSessionGeneration: request.SIMSessionGeneration, Action: request.Action, State: "verified"}
	if request.Action == SIMPINStatus {
		attempts := uint32(3)
		response.State, response.AttemptsRemaining = "retry_counter", &attempts
	}
	return response
}

func boolPointer(value bool) *bool { return &value }

func TestSIMPINContractsFenceReaderAndModemInsertions(t *testing.T) {
	reader := SIMPINRequest{OperationID: "pin-operation-1", ProcessGeneration: "process-1",
		CardID: "89010000000000000001", ReaderName: "reader-a", SIMSessionGeneration: "sim-session-1",
		Action: SIMPINVerify, PIN: "1234"}
	if err := reader.Validate(); err != nil {
		t.Fatal(err)
	}
	modem := SIMPINRequest{OperationID: "pin-operation-2", ProcessGeneration: "process-1",
		CardID: "89010000000000000001", AttachmentID: "attachment-1", EquipmentID: "862547055201716",
		SIMSessionGeneration: "sim-session-1", Action: SIMPINChange, PIN: "1234", NewPIN: "5678"}
	if err := modem.Validate(); err != nil {
		t.Fatal(err)
	}
	setEnabled := SIMPINCommand{OperationID: "pin-operation-3", CardID: reader.CardID,
		ReaderName: reader.ReaderName, Action: SIMPINSetEnabled, PIN: "1234", Enabled: boolPointer(false),
		PreflightOperationID: "pin-status-operation"}
	if err := setEnabled.Validate(); err != nil {
		t.Fatal(err)
	}
	status := SIMPINCommand{OperationID: "pin-status-operation", CardID: reader.CardID,
		ReaderName: reader.ReaderName, Action: SIMPINStatus}
	if err := status.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSIMPINResponseRequiresExactTypedOutcome(t *testing.T) {
	attempts := uint32(3)
	response := SIMPINResponse{OperationID: "pin-status-operation", CardID: "89010000000000000001",
		ReaderName: "reader-a", SIMSessionGeneration: "sim-session-1", Action: SIMPINStatus,
		State: "retry_counter", AttemptsRemaining: &attempts}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	response.Action = SIMPINVerify
	if err := response.Validate(); err == nil {
		t.Fatal("retry counter was accepted as a verify outcome")
	}
	response.Action, response.AttemptsRemaining = SIMPINStatus, nil
	if err := response.Validate(); err == nil {
		t.Fatal("retry counter without a count was accepted")
	}
	response.AttemptsRemaining = &attempts
	response.State = "unavailable"
	if err := response.Validate(); err == nil {
		t.Fatal("unavailable status without typed failure was accepted")
	}
	response.Action, response.AttemptsRemaining = SIMPINVerify, nil
	response.State = "unknown"
	response.Failure = &RemoteError{Kind: "transport", Code: "sim_pin_outcome_unknown"}
	if err := response.Validate(); err != nil {
		t.Fatalf("unknown credential outcome was rejected: %v", err)
	}
}

func TestSIMPINContractsRejectAmbiguousTargetsAndUnsafeFields(t *testing.T) {
	base := SIMPINRequest{OperationID: "pin-operation-1", ProcessGeneration: "process-1",
		CardID: "89010000000000000001", ReaderName: "reader-a", SIMSessionGeneration: "sim-session-1",
		Action: SIMPINVerify, PIN: "1234"}
	tests := map[string]func(*SIMPINRequest){
		"missing target":               func(value *SIMPINRequest) { value.ReaderName = "" },
		"two targets":                  func(value *SIMPINRequest) { value.EquipmentID = "862547055201716" },
		"missing generation":           func(value *SIMPINRequest) { value.SIMSessionGeneration = "" },
		"non numeric PIN":              func(value *SIMPINRequest) { value.PIN = "12ab" },
		"verify with new PIN":          func(value *SIMPINRequest) { value.NewPIN = "5678" },
		"change without new PIN":       func(value *SIMPINRequest) { value.Action = SIMPINChange },
		"enable without desired state": func(value *SIMPINRequest) { value.Action = SIMPINSetEnabled },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatalf("unsafe request accepted: %+v", value)
			}
		})
	}
}

func TestSIMPINEnvelopeRoundTripRejectsCredentialCrossingOtherKinds(t *testing.T) {
	request := SIMPINRequest{OperationID: "pin-operation-1", ProcessGeneration: "process-1",
		CardID: "89010000000000000001", ReaderName: "reader-a", SIMSessionGeneration: "sim-session-1",
		Action: SIMPINVerify, PIN: "1234"}
	payload := `{"kind":"sim_pin_request","request_id":"request-1","sim_pin_request":{"operation_id":"pin-operation-1","process_generation":"process-1","card_id":"89010000000000000001","reader_name":"reader-a","sim_session_generation":"sim-session-1","action":"verify","pin":"1234"}}`
	message, err := decodeEnvelope([]byte(payload))
	if err != nil || message.validate() != nil || message.SIMPINRequest == nil {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	if request.Validate() != nil {
		t.Fatal("request contract rejected valid fixture")
	}
	bad := `{"kind":"modem_request","request_id":"request-1","modem_request":{},"sim_pin_request":{"operation_id":"pin-operation-1"}}`
	if message, err := decodeEnvelope([]byte(bad)); err == nil && message.validate() == nil {
		t.Fatal("PIN fields crossed into modem envelope")
	}
}

func TestSIMPINStatusAndVerifyUseAgentWSS(t *testing.T) {
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
		done <- (Client{URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/v1/agent/ws",
			Token: testToken, Hello: Hello{SchemaVersion: SchemaVersion, AgentID: "agent-1", ProcessGeneration: "process-1"},
			Authenticator: &fakeAuthenticator{}, PIN: fakeSIMPINExecutor{}, Health: func() TopologySnapshot {
				return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}}
			}, HealthEvery: time.Second, OperationTimeout: time.Second}).Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if status, found := server.Status("agent-1"); found && len(status.Capabilities) == 1 && status.Capabilities[0] == simPINFeature {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("SIM PIN Agent stopped before connect: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("SIM PIN Agent did not connect with negotiated capability")
		}
		time.Sleep(time.Millisecond)
	}
	statusRequest := SIMPINRequest{OperationID: "pin-status-operation", ProcessGeneration: "process-1",
		CardID: "89010000000000000001", ReaderName: "reader-a", SIMSessionGeneration: "session-1", Action: SIMPINStatus}
	status, err := server.ExecuteSIMPIN(context.Background(), "agent-1", "process-1", statusRequest)
	if err != nil || status.State != "retry_counter" || status.AttemptsRemaining == nil || *status.AttemptsRemaining != 3 {
		select {
		case clientErr := <-done:
			t.Fatalf("status=%+v err=%v client_error=%v", status, err, clientErr)
		default:
			t.Fatalf("status=%+v err=%v", status, err)
		}
	}
	verify := statusRequest
	verify.OperationID, verify.Action, verify.PIN = "pin-verify-operation", SIMPINVerify, "1234"
	result, err := server.ExecuteSIMPIN(context.Background(), "agent-1", "process-1", verify)
	if err != nil || result.State != "verified" {
		t.Fatalf("verify=%+v err=%v", result, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SIM PIN Agent did not stop")
	}
}
