package cellularmessages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

type testCatalog struct{ line linecatalog.Line }

func (catalog testCatalog) Get(id string) (linecatalog.Line, error) {
	if id != catalog.line.ID {
		return linecatalog.Line{}, linecatalog.ErrNotFound
	}
	return catalog.line, nil
}

type testAgents struct {
	requests       []agentlink.ModemRequest
	list           []agentlink.ModemSMSMessage
	failure        error
	resolveFailure error
}

type testAllowanceAuthorizer struct {
	err   error
	calls int
}

func (authorizer *testAllowanceAuthorizer) AuthorizeDispatch(string, string, string, string, string, string, string, string) error {
	authorizer.calls++
	return authorizer.err
}

func (runtime *testAgents) ResolveModemTargetForAction(equipmentID, cardID string, action agentlink.ModemAction) (agentlink.ModemTarget, error) {
	if runtime.resolveFailure != nil {
		return agentlink.ModemTarget{}, runtime.resolveFailure
	}
	if equipmentID != "862547055201716" || cardID != "8985200000000000001" {
		return agentlink.ModemTarget{}, agentlink.ErrModemOffline
	}
	return agentlink.ModemTarget{
		AgentID: "agent-1", ProcessGeneration: "generation-1", AttachmentID: "attachment-1",
		EquipmentID: equipmentID, CardID: cardID,
	}, nil
}

func (runtime *testAgents) ExecuteModem(_ context.Context, agentID, generation string, request agentlink.ModemRequest) (agentlink.ModemResponse, error) {
	runtime.requests = append(runtime.requests, request)
	response := agentlink.ModemResponse{
		OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
	}
	if runtime.failure != nil {
		return response, runtime.failure
	}
	if request.Action == agentlink.ModemSMSList {
		response.SMS = &agentlink.ModemSMSResult{State: "listed", Messages: runtime.list}
	} else {
		response.SMS = &agentlink.ModemSMSResult{State: "submitted", References: []int{0, 17}}
	}
	return response, nil
}

func TestCellularSendUsesExactAgentTargetAndPersistsEveryReference(t *testing.T) {
	service, store, agents := testService(t)
	handler := serviceMux(service)
	payload := SendRequest{
		OperationID: "sms-operation-1", MessageID: "message-1", Recipient: "+15550100124", Body: "hello 世界",
		ExpectedCardID: "8985200000000000001",
	}
	response := postJSON(t, handler, "/v1/lines/line-1/cellular/messages", payload)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(agents.requests) != 1 {
		t.Fatalf("requests=%d", len(agents.requests))
	}
	request := agents.requests[0]
	if request.Action != agentlink.ModemSMSSend || request.OperationID != payload.OperationID ||
		request.Number != payload.Recipient || request.Body != payload.Body || request.AttachmentID != "attachment-1" {
		t.Fatalf("request=%+v", request)
	}
	records, err := store.List("line-1", 100)
	if err != nil || len(records) != 2 || records[0].Kind != providermessages.KindSubmitted ||
		records[0].MessageID != "message-1" || records[0].RPMR != 0 || records[0].CallID != "cellular-mr-0" ||
		records[1].RPMR != 17 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	// The same browser operation is safe to replay: Agent owns submission
	// idempotency and Core's event IDs do not duplicate durable records.
	response = postJSON(t, handler, "/v1/lines/line-1/cellular/messages", payload)
	records, err = store.List("line-1", 100)
	if response.Code != http.StatusOK || err != nil || len(records) != 2 {
		t.Fatalf("retry status=%d records=%+v err=%v", response.Code, records, err)
	}
}

func TestCellularListPersistsReceivedAndDeliveryFacts(t *testing.T) {
	service, store, agents := testService(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	agents.list = []agentlink.ModemSMSMessage{
		{Index: 1, State: "received", Direction: "in", Peer: "+15550100123", Body: "hello", ObservedAt: now, Fingerprint: repeatedHex('a')},
		{Index: 2, State: "delivery", Direction: "in", Peer: "+15550100124", ObservedAt: now, Fingerprint: repeatedHex('b'), Reference: 0, Delivery: "delivered"},
		{Index: 3, State: "stored", Direction: "out", Peer: "+15550100124", Body: "old", ObservedAt: now, Fingerprint: repeatedHex('c')},
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/lines/line-1/cellular/messages", nil)
	response := httptest.NewRecorder()
	serviceMux(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	records, err := store.List("line-1", 100)
	if err != nil || len(records) != 2 || records[0].Kind != providermessages.KindReceived ||
		records[0].Body != "hello" || records[1].Kind != providermessages.KindDelivery ||
		records[1].CallID != "cellular-mr-0" || records[1].State != "delivered" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func TestCellularUncertainSubmitIsConflictAndPreservesBrowserIdentity(t *testing.T) {
	service, _, agents := testService(t)
	agents.failure = &agentlink.RemoteError{Kind: "failed", Code: "modem_sms_submit_uncertain"}
	response := postJSON(t, serviceMux(service), "/v1/lines/line-1/cellular/messages", SendRequest{
		OperationID: "sms-operation-1", MessageID: "message-1", Recipient: "+15550100124", Body: "hello",
		ExpectedCardID: "8985200000000000001",
	})
	if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"modem_sms_submit_uncertain\"}\n" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	agents.resolveFailure = agentlink.ErrAgentOffline
	response = postJSON(t, serviceMux(service), "/v1/lines/line-1/cellular/messages", SendRequest{
		OperationID: "sms-operation-1", MessageID: "message-1", Recipient: "+15550100124", Body: "hello",
		ExpectedCardID: "8985200000000000001",
	})
	if response.Code != http.StatusConflict || len(agents.requests) != 1 {
		t.Fatalf("retry status=%d requests=%d body=%s", response.Code, len(agents.requests), response.Body.String())
	}
}

func TestCellularSendRejectsMissingOrChangedExpectedCardBeforeAgent(t *testing.T) {
	service, _, agents := testService(t)
	for _, cardID := range []string{"", "8985200000000000999"} {
		response := postJSON(t, serviceMux(service), "/v1/lines/line-1/cellular/messages", SendRequest{
			OperationID: "sms-card-" + cardID, MessageID: "message-card-" + cardID,
			Recipient: "+15550100124", Body: "hello", ExpectedCardID: cardID,
		})
		if response.Code != http.StatusConflict {
			t.Fatalf("card=%q status=%d body=%s", cardID, response.Code, response.Body.String())
		}
	}
	if len(agents.requests) != 0 {
		t.Fatalf("Agent received requests=%d", len(agents.requests))
	}
}

func TestCellularAllowanceDispatchIsRevokedBeforeAgent(t *testing.T) {
	service, _, agents := testService(t)
	authorizer := &testAllowanceAuthorizer{err: errors.New("query closed")}
	if err := service.BindAllowanceDispatchAuthorizer(authorizer); err != nil {
		t.Fatal(err)
	}
	response := postJSON(t, serviceMux(service), "/v1/lines/line-1/cellular/messages", SendRequest{
		OperationID: "allowance-send", MessageID: "allowance-message", Recipient: "6700", Body: "BAL",
		ExpectedCardID: "8985200000000000001", AllowanceQueryID: "allowance-query",
	})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "allowance_query_changed") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if authorizer.calls != 1 || len(agents.requests) != 0 {
		t.Fatalf("authorizer=%d Agent requests=%d", authorizer.calls, len(agents.requests))
	}
}

func testService(t *testing.T) (*Service, *providermessages.Store, *testAgents) {
	t.Helper()
	store, err := providermessages.OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	operations, err := OpenOperationStore(filepath.Join(t.TempDir(), "operations.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operations.Close() })
	agents := &testAgents{}
	service, err := New(testCatalog{line: linecatalog.Line{
		SchemaVersion: 1, ID: "line-1", CardID: "8985200000000000001", Enabled: true,
		SIM: linecatalog.SIMConfig{IMEI: "862547055201716"},
	}}, agents, store, operations)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, agents
}

func serviceMux(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /v1/lines/{lineID}/cellular/messages", service)
	mux.Handle("POST /v1/lines/{lineID}/cellular/messages", service)
	return mux
}

func postJSON(t *testing.T, handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(value)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func repeatedHex(character byte) string { return string(bytes.Repeat([]byte{character}, 64)) }
