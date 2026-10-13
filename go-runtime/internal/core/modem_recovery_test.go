package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type modemRecoveryStub struct {
	calls   int
	command agentlink.ModemRecoveryCommand
	err     error
}

func TestModemRecoveryDisconnectAfterDispatchStaysUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const token = "0123456789abcdef0123456789abcdef"
	server, err := agentlink.NewServer(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return token, nil }))
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	defer host.Close()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(host.URL, "http"), &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer " + token}, "X-Mdd-Agent-Id": {"agent-1"}, "X-Mdd-Agent-Capabilities": {"modem-recovery-v1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	if err := wsjson.Write(ctx, socket, map[string]any{"kind": "hello", "hello": agentlink.Hello{SchemaVersion: 1, AgentID: "agent-1", ProcessGeneration: "process-1"}}); err != nil {
		t.Fatal(err)
	}
	var hello map[string]any
	if err := wsjson.Read(ctx, socket, &hello); err != nil {
		t.Fatal(err)
	}
	topology := agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, ModemCondition: agentlink.ModemReady,
		Modems: []agentlink.ModemFact{{AttachmentID: "attachment-1", EquipmentID: "862547055201716", Condition: "ready",
			SIM:     agentlink.ModemSIMFact{State: "ready", ICCID: "8985200000000000001", SessionGeneration: "session-1"},
			AT:      agentlink.ModemATControlFact{State: "ready", Port: "COM3"},
			Network: agentlink.ModemNetworkFact{Registration: "roaming", SoftwareRadio: "on", HardwareRadio: "on", Data: "disconnected", DataGuard: "protected"}}}}
	revision, err := topology.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, socket, map[string]any{"kind": "health", "health": agentlink.HealthReport{SchemaVersion: 1, Sequence: 1, TopologyRevision: revision, Topology: &topology}}); err != nil {
		t.Fatal(err)
	}
	received := make(chan agentlink.ModemRecoveryRequest, 1)
	readError := make(chan error, 1)
	go func() {
		var envelope struct {
			Kind      string                         `json:"kind"`
			Request   agentlink.ModemRecoveryRequest `json:"modem_recovery_request"`
			RequestID string                         `json:"request_id"`
		}
		if err := wsjson.Read(ctx, socket, &envelope); err != nil {
			readError <- err
			return
		}
		received <- envelope.Request
		// Receipt is deliberately lost after the actual command reached the Agent.
		_ = socket.CloseNow()
	}()
	for {
		if status, found := server.Status("agent-1"); found && status.Topology != nil {
			break
		}
		select {
		case err := <-readError:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Enabled: true, CardID: "8985200000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewModemRecoveryHandler(server, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"lost-receipt","expected_card_id":"8985200000000000001","equipment_id":"862547055201716","expected_target":{"agent_id":"agent-1","process_generation":"process-1","attachment_id":"attachment-1","equipment_id":"862547055201716","card_id":"8985200000000000001","sim_session_generation":"session-1"}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/lines/line-1/cellular/soft-restart", strings.NewReader(payload)).WithContext(ctx)
	request.SetPathValue("lineID", "line-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	select {
	case command := <-received:
		if command.OperationID != "lost-receipt" {
			t.Fatalf("wrong command: %+v", command)
		}
	case err := <-readError:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var outcome map[string]string
	if json.Unmarshal(response.Body.Bytes(), &outcome) != nil || response.Code != http.StatusAccepted || outcome["state"] != "unknown" || outcome["operation_id"] != "lost-receipt" {
		t.Fatalf("DISPATCHED_RECOVERY_DOWNGRADED: HTTP%d %s", response.Code, response.Body.String())
	}
	receipt, found, err := store.GetOperation("lost-receipt")
	if err != nil || !found || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/v1/operations/lost-receipt", nil)
	statusRequest.SetPathValue("operationID", "lost-receipt")
	status := httptest.NewRecorder()
	linecatalog.NewOperationHandler(store).ServeHTTP(status, statusRequest)
	var persisted struct {
		OperationID string `json:"operation_id"`
		State       string `json:"state"`
	}
	if json.Unmarshal(status.Body.Bytes(), &persisted) != nil || status.Code != http.StatusOK || persisted.OperationID != "lost-receipt" || persisted.State != "unknown" {
		t.Fatalf("status=%s", status.Body.String())
	}
}

func (stub *modemRecoveryStub) ExecuteModemRecoveryCommand(_ context.Context,
	command agentlink.ModemRecoveryCommand,
) (agentlink.ModemRecoveryResponse, error) {
	stub.calls++
	stub.command = command
	if stub.err != nil {
		return agentlink.ModemRecoveryResponse{}, stub.err
	}
	return agentlink.ModemRecoveryResponse{OperationID: command.OperationID, EquipmentID: command.EquipmentID,
		CardID: command.CardID, AttachmentID: "attachment-1", SIMSessionGeneration: "session-1",
		Action: command.Action, State: "accepted"}, nil
}

func TestModemRecoveryHandlerRecordsUnavailableBeforeAgentAsFailed(t *testing.T) {
	store, _ := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	defer store.Close()
	_, _ = store.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Enabled: true,
		CardID: "8985200000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}})
	stub := &modemRecoveryStub{err: agentlink.ErrModemOffline}
	handler, _ := NewModemRecoveryHandler(stub, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/lines/line-1/cellular/soft-restart",
		strings.NewReader(`{"operation_id":"restart-offline","expected_card_id":"8985200000000000001","equipment_id":"862547055201716","expected_target":{"agent_id":"agent-1","process_generation":"process-1","attachment_id":"attachment-1","equipment_id":"862547055201716","card_id":"8985200000000000001","sim_session_generation":"session-1"}}`))
	request.SetPathValue("lineID", "line-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	receipt, found, err := store.GetOperation("restart-offline")
	if response.Code != http.StatusPreconditionFailed || err != nil || !found || receipt.State != linecatalog.OperationFailed ||
		receipt.ErrorCode != "modem_recovery_target_unavailable" {
		t.Fatalf("status=%d receipt=%+v found=%t err=%v", response.Code, receipt, found, err)
	}
}

func TestModemRecoveryHandlerIsExactDurableAndIdempotent(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _ = store.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Enabled: true,
		CardID: "8985200000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}})
	stub := &modemRecoveryStub{}
	handler, _ := NewModemRecoveryHandler(stub, store)
	payload := `{"operation_id":"restart-1","expected_card_id":"8985200000000000001","equipment_id":"862547055201716","expected_target":{"agent_id":"agent-1","process_generation":"process-1","attachment_id":"attachment-1","equipment_id":"862547055201716","card_id":"8985200000000000001","sim_session_generation":"session-1"}}`
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/lines/line-1/cellular/soft-restart", strings.NewReader(payload))
		request.SetPathValue("lineID", "line-1")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("attempt=%d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	if stub.calls != 1 || stub.command.Action != agentlink.ModemSoftRestart {
		t.Fatalf("calls=%d command=%+v", stub.calls, stub.command)
	}
	receipt, found, err := store.GetOperation("restart-1")
	if err != nil || !found || receipt.State != linecatalog.OperationSucceeded ||
		receipt.Kind != linecatalog.OperationModemRecovery || receipt.SIMSessionGeneration != "session-1" {
		t.Fatalf("receipt=%+v found=%t err=%v", receipt, found, err)
	}
}

func TestModemRecoveryHandlerRecordsTransportUnknownWithoutRetry(t *testing.T) {
	store, _ := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	defer store.Close()
	_, _ = store.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Enabled: true,
		CardID: "8985200000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}})
	stub := &modemRecoveryStub{err: context.DeadlineExceeded}
	handler, _ := NewModemRecoveryHandler(stub, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/lines/line-1/cellular/soft-restart",
		strings.NewReader(`{"operation_id":"restart-unknown","expected_card_id":"8985200000000000001","equipment_id":"862547055201716","expected_target":{"agent_id":"agent-1","process_generation":"process-1","attachment_id":"attachment-1","equipment_id":"862547055201716","card_id":"8985200000000000001","sim_session_generation":"session-1"}}`))
	request.SetPathValue("lineID", "line-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	receipt, found, err := store.GetOperation("restart-unknown")
	if response.Code != http.StatusAccepted || stub.calls != 1 || err != nil || !found || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("status=%d calls=%d receipt=%+v found=%t err=%v", response.Code, stub.calls, receipt, found, err)
	}
	var outcome map[string]string
	if json.Unmarshal(response.Body.Bytes(), &outcome) != nil || outcome["operation_id"] != "restart-unknown" || outcome["state"] != "unknown" {
		t.Fatalf("unknown response lost original operation: %s", response.Body.String())
	}
}
