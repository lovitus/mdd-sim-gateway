package euiccprofiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const (
	testEID   = "89049032000000000000000000000001"
	testICCID = "8944000000000000001"
)

type fakeAgents struct {
	statuses             []agentlink.ConnectionStatus
	commands             []agentlink.EUICCProfileCommand
	result               agentlink.EUICCProfileResponse
	err                  error
	downloadCommands     []agentlink.EUICCDownloadCommand
	downloadResult       agentlink.EUICCDownloadResponse
	downloadErr          error
	discoveryCommands    []agentlink.EUICCDiscoveryCommand
	discoveryResult      agentlink.EUICCDiscoveryResponse
	discoveryErr         error
	notificationCommands []agentlink.EUICCNotificationCommand
	notificationResult   agentlink.EUICCNotificationResponse
	notificationErr      error
}

func (agents *fakeAgents) ExecuteEUICCNotificationCommand(_ context.Context,
	command agentlink.EUICCNotificationCommand) (agentlink.EUICCNotificationResponse, error) {
	agents.notificationCommands = append(agents.notificationCommands, command)
	result := agents.notificationResult
	result.OperationID = command.OperationID
	return result, agents.notificationErr
}

func (agents *fakeAgents) ExecuteEUICCDiscoveryCommand(_ context.Context,
	command agentlink.EUICCDiscoveryCommand) (agentlink.EUICCDiscoveryResponse, error) {
	agents.discoveryCommands = append(agents.discoveryCommands, command)
	return agents.discoveryResult, agents.discoveryErr
}

func (agents *fakeAgents) ExecuteEUICCDownloadCommand(_ context.Context,
	command agentlink.EUICCDownloadCommand) (agentlink.EUICCDownloadResponse, error) {
	agents.downloadCommands = append(agents.downloadCommands, command)
	return agents.downloadResult, agents.downloadErr
}

type fakeCatalog struct {
	snapshot linecatalog.Snapshot
	err      error
}

func (catalog fakeCatalog) Snapshot() (linecatalog.Snapshot, error) {
	return catalog.snapshot, catalog.err
}

type fakeProviderStatus struct {
	statuses map[string]vowifiipc.Snapshot
	err      error
}

func (provider fakeProviderStatus) Status(_ context.Context, lineID string) (vowifiipc.Snapshot, error) {
	if provider.err != nil {
		return vowifiipc.Snapshot{}, provider.err
	}
	return provider.statuses[lineID], nil
}

func (agents *fakeAgents) Statuses() []agentlink.ConnectionStatus { return agents.statuses }

func (agents *fakeAgents) ExecuteEUICCProfileCommand(_ context.Context,
	command agentlink.EUICCProfileCommand) (agentlink.EUICCProfileResponse, error) {
	agents.commands = append(agents.commands, command)
	return agents.result, agents.err
}

func TestInventoryPreservesMultiReaderIdentityAndProfileMetadata(t *testing.T) {
	agents := &fakeAgents{statuses: []agentlink.ConnectionStatus{
		{AgentID: "agent-b", ProcessGeneration: "process-b", LastSeen: time.Unix(20, 0).UTC(), Topology: topology("reader-b", "insertion-b", "89049032000000000000000000000002", nil)},
		{AgentID: "agent-a", ProcessGeneration: "process-a", LastSeen: time.Unix(10, 0).UTC(), Topology: topology("reader-a", "insertion-a", testEID, []agentlink.EUICCProfileFact{{
			ICCID: testICCID, State: agentlink.EUICCProfileDisabled, Nickname: "travel",
			ServiceProviderName: "carrier", ProfileName: "plan",
		}})},
	}}
	service, _ := New(agents)
	service.now = func() time.Time { return time.Unix(30, 0).UTC() }
	response := httptest.NewRecorder()
	service.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/euiccs", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		At     time.Time        `json:"at"`
		EUICCs []InventoryEntry `json:"euiccs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.At.Equal(time.Unix(30, 0).UTC()) || len(payload.EUICCs) != 2 ||
		payload.EUICCs[0].EUICC.EID != testEID || payload.EUICCs[0].SessionGeneration != "insertion-a" ||
		payload.EUICCs[0].EUICC.Profiles[0].Nickname != "travel" || !payload.EUICCs[0].EUICC.ProfileManagement {
		t.Fatalf("inventory=%+v", payload)
	}
}

func TestInventoryExpandsSecureElementsFromOneReader(t *testing.T) {
	secondEID := "89049032000000000000000000000002"
	agents := &fakeAgents{statuses: []agentlink.ConnectionStatus{{
		AgentID: "agent-a", ProcessGeneration: "process-a", Topology: &agentlink.TopologySnapshot{
			ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{{
				ReaderName: "reader-a", CardPresent: true, SessionGeneration: "insertion-a",
				IdentityState: agentlink.CardIdentified, SecureElements: []agentlink.EUICCSlotFact{
					{SlotID: "se0", Label: "SE1", EUICC: agentlink.EUICCFact{EID: testEID, ProfilesAvailable: true, Profiles: []agentlink.EUICCProfileFact{}}},
					{SlotID: "se1", Label: "SE2", EUICC: agentlink.EUICCFact{EID: secondEID, ProfilesAvailable: true, Profiles: []agentlink.EUICCProfileFact{}}},
				},
			}},
		},
	}}}
	service, _ := New(agents)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/euiccs", nil))
	var payload struct {
		EUICCs []InventoryEntry `json:"euiccs"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || len(payload.EUICCs) != 2 ||
		payload.EUICCs[0].SlotID != "se0" || payload.EUICCs[0].SlotLabel != "SE1" ||
		payload.EUICCs[1].SlotID != "se1" || payload.EUICCs[1].EUICC.EID != secondEID {
		t.Fatalf("status=%d inventory=%+v body=%s", response.Code, payload, response.Body.String())
	}
}

func TestProfileMutationForwardsExactIntentAndReportsRefreshPending(t *testing.T) {
	agents := &fakeAgents{result: agentlink.EUICCProfileResponse{
		OperationID: "operation-1", SessionGeneration: "insertion-a", EID: testEID, ICCID: testICCID,
		Action: agentlink.EUICCProfileEnable, Outcome: agentlink.EUICCProfileRefreshPending, Changed: true,
	}}
	service, _ := New(agents)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/euiccs/{eid}/profiles/{iccid}/{action}", service)
	response := post(t, mux, "/v1/euiccs/"+testEID+"/profiles/"+testICCID+"/enable", map[string]any{
		"operation_id": "operation-1", "expected_state": "disabled",
	})
	if response.Code != http.StatusAccepted || len(agents.commands) != 1 {
		t.Fatalf("status=%d commands=%+v body=%s", response.Code, agents.commands, response.Body.String())
	}
	command := agents.commands[0]
	if command.OperationID != "operation-1" || command.EID != testEID || command.ICCID != testICCID ||
		command.Action != agentlink.EUICCProfileEnable || command.ExpectedState != agentlink.EUICCProfileDisabled {
		t.Fatalf("command=%+v", command)
	}
}

func TestProfileNicknameForwardsDesiredAndExpectedValues(t *testing.T) {
	agents := &fakeAgents{result: agentlink.EUICCProfileResponse{
		OperationID: "nickname-1", SessionGeneration: "insertion-a", EID: testEID, ICCID: testICCID,
		Action: agentlink.EUICCProfileNickname, Outcome: agentlink.EUICCProfileRefreshPending, Changed: true,
	}}
	service, _ := New(agents)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/euiccs/{eid}/profiles/{iccid}/{action}", service)
	response := post(t, mux, "/v1/euiccs/"+testEID+"/profiles/"+testICCID+"/nickname", map[string]any{
		"operation_id": "nickname-1", "nickname": "旅行", "expected_nickname": "old",
	})
	if response.Code != http.StatusAccepted || len(agents.commands) != 1 {
		t.Fatalf("status=%d commands=%+v body=%s", response.Code, agents.commands, response.Body.String())
	}
	command := agents.commands[0]
	if command.Action != agentlink.EUICCProfileNickname || command.Nickname != "旅行" ||
		command.ExpectedNickname != "old" || command.ExpectedState != "" {
		t.Fatalf("command=%+v", command)
	}

	tooLong := strings.Repeat("a", 65)
	response = post(t, mux, "/v1/euiccs/"+testEID+"/profiles/"+testICCID+"/nickname", map[string]any{
		"operation_id": "nickname-2", "nickname": tooLong, "expected_nickname": "old",
	})
	if response.Code != http.StatusBadRequest || len(agents.commands) != 1 {
		t.Fatalf("long nickname status=%d commands=%+v body=%s", response.Code, agents.commands, response.Body.String())
	}
	response = post(t, mux, "/v1/euiccs/"+testEID+"/profiles/"+testICCID+"/nickname", map[string]any{
		"operation_id": "nickname-3", "expected_nickname": "old",
	})
	if response.Code != http.StatusBadRequest || len(agents.commands) != 1 {
		t.Fatalf("missing nickname status=%d commands=%+v body=%s", response.Code, agents.commands, response.Body.String())
	}
}

func TestProfileMutationRejectsStaleIntentAndPreservesTypedErrors(t *testing.T) {
	agents := &fakeAgents{}
	service, _ := New(agents)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/euiccs/{eid}/profiles/{iccid}/{action}", service)
	response := post(t, mux, "/v1/euiccs/"+testEID+"/profiles/"+testICCID+"/enable", map[string]any{
		"operation_id": "operation-1", "expected_state": "enabled",
	})
	if response.Code != http.StatusBadRequest || len(agents.commands) != 0 {
		t.Fatalf("stale status=%d commands=%+v body=%s", response.Code, agents.commands, response.Body.String())
	}
	agents.err = &agentlink.RemoteError{Kind: "conflict", Code: "euicc_profile_state_changed"}
	response = post(t, mux, "/v1/euiccs/"+testEID+"/profiles/"+testICCID+"/enable", map[string]any{
		"operation_id": "operation-2", "expected_state": "disabled",
	})
	if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"euicc_profile_state_changed\"}\n" {
		t.Fatalf("typed status=%d body=%s", response.Code, response.Body.String())
	}
	agents.err = errors.New("internal")
	response = post(t, mux, "/v1/euiccs/"+testEID+"/profiles/"+testICCID+"/enable", map[string]any{
		"operation_id": "operation-3", "expected_state": "disabled",
	})
	if response.Code != http.StatusBadGateway || response.Body.String() != "{\"code\":\"euicc_profile_operation_failed\"}\n" {
		t.Fatalf("internal status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDownloadStartForwardsOneUseSecretsWithoutEchoingThem(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	agents := &fakeAgents{downloadResult: agentlink.EUICCDownloadResponse{
		OperationID: "download-1", SessionGeneration: "insertion-a", EID: testEID,
		Action: agentlink.EUICCDownloadStart, Job: &agentlink.EUICCDownloadJob{
			State: agentlink.EUICCDownloadQueued, Stage: agentlink.EUICCDownloadStageQueued,
			StartedAt: now, UpdatedAt: now,
		},
	}}
	service, _ := New(agents)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/euiccs/{eid}/downloads", service)
	activation, confirmation := "LPA:1$example.com$matching-id", "confirm-once"
	response := post(t, mux, "/v1/euiccs/"+testEID+"/downloads", map[string]any{
		"operation_id": "download-1", "activation_code": activation,
		"confirmation_code": confirmation, "imei": "123456789012345",
	})
	if response.Code != http.StatusAccepted || len(agents.downloadCommands) != 1 {
		t.Fatalf("status=%d commands=%+v body=%s", response.Code, agents.downloadCommands, response.Body.String())
	}
	command := agents.downloadCommands[0]
	if command.OperationID != "download-1" || command.EID != testEID ||
		command.ActivationCode != activation || command.ConfirmationCode != confirmation || command.IMEI != "123456789012345" {
		t.Fatalf("command=%+v", command)
	}
	if bytes.Contains(response.Body.Bytes(), []byte(activation)) || bytes.Contains(response.Body.Bytes(), []byte(confirmation)) {
		t.Fatalf("one-use secret was echoed: %s", response.Body.String())
	}
}

func TestDiscoveryForwardsOptionalInputsAndReturnsTypedEvents(t *testing.T) {
	agents := &fakeAgents{discoveryResult: agentlink.EUICCDiscoveryResponse{
		OperationID: "discovery-1", SessionGeneration: "insertion-a", EID: testEID,
		SMDS: "lpa.ds.gsma.com", Entries: []agentlink.EUICCDiscoveryEntry{{
			EventID: "event-1", RSPServerAddress: "rsp.example.com",
		}},
	}}
	service, _ := New(agents)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/euiccs/{eid}/discovery", service)
	response := post(t, mux, "/v1/euiccs/"+testEID+"/discovery", map[string]any{
		"operation_id": "discovery-1", "smds": "lpa.ds.gsma.com", "imei": "123456789012345",
	})
	if response.Code != http.StatusOK || len(agents.discoveryCommands) != 1 {
		t.Fatalf("status=%d commands=%+v body=%s", response.Code, agents.discoveryCommands, response.Body.String())
	}
	command := agents.discoveryCommands[0]
	if command.OperationID != "discovery-1" || command.EID != testEID || command.SMDS != "lpa.ds.gsma.com" ||
		command.IMEI != "123456789012345" || !strings.Contains(response.Body.String(), "rsp.example.com") {
		t.Fatalf("command=%+v body=%s", command, response.Body.String())
	}

	response = post(t, mux, "/v1/euiccs/"+testEID+"/discovery", map[string]any{
		"operation_id": "discovery-2", "imei": "123",
	})
	if response.Code != http.StatusBadRequest || len(agents.discoveryCommands) != 1 {
		t.Fatalf("invalid status=%d commands=%+v body=%s", response.Code, agents.discoveryCommands, response.Body.String())
	}
}

func TestNotificationInventoryGeneratesIdentityAndReturnsReadOnlyTypedEntries(t *testing.T) {
	agents := &fakeAgents{notificationResult: agentlink.EUICCNotificationResponse{
		SessionGeneration: "insertion-a", EID: testEID,
		Entries: []agentlink.EUICCNotificationEntry{{
			SequenceNumber: 7, Event: "enable", ICCID: testICCID, Address: "notify.example.com",
		}},
	}}
	service, _ := New(agents)
	mux := http.NewServeMux()
	mux.Handle("GET /v1/euiccs/{eid}/notifications", service)
	request := httptest.NewRequest(http.MethodGet, "/v1/euiccs/"+testEID+"/notifications", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(agents.notificationCommands) != 1 {
		t.Fatalf("status=%d commands=%+v body=%s", response.Code, agents.notificationCommands, response.Body.String())
	}
	command := agents.notificationCommands[0]
	if command.EID != testEID || !strings.HasPrefix(command.OperationID, "notification-") ||
		!strings.Contains(response.Body.String(), `"sequence_number":7`) ||
		!strings.Contains(response.Body.String(), `"address":"notify.example.com"`) {
		t.Fatalf("command=%+v body=%s", command, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/euiccs/"+testEID+"/notifications", strings.NewReader("{}"))
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || len(agents.notificationCommands) != 1 {
		t.Fatalf("mutation status=%d commands=%+v", response.Code, agents.notificationCommands)
	}
}

func TestNotificationDeliveryRequiresConfirmationAndPreservesExpectedMetadata(t *testing.T) {
	agents := &fakeAgents{notificationResult: agentlink.EUICCNotificationResponse{
		SessionGeneration: "insertion-a", EID: testEID, Acknowledged: true, Removed: true,
	}}
	service, _ := New(agents)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/euiccs/{eid}/notifications/{sequence}/deliver", service)
	path := "/v1/euiccs/" + testEID + "/notifications/7/deliver"
	response := post(t, mux, path, map[string]any{
		"confirmed": false, "event": "enable", "iccid": testICCID, "address": "notify.example.com",
	})
	if response.Code != http.StatusBadRequest || len(agents.notificationCommands) != 0 {
		t.Fatalf("unconfirmed status=%d commands=%+v body=%s", response.Code, agents.notificationCommands, response.Body.String())
	}
	response = post(t, mux, path, map[string]any{
		"confirmed": true, "event": "enable", "iccid": testICCID, "address": "notify.example.com",
	})
	if response.Code != http.StatusOK || len(agents.notificationCommands) != 1 {
		t.Fatalf("confirmed status=%d commands=%+v body=%s", response.Code, agents.notificationCommands, response.Body.String())
	}
	command := agents.notificationCommands[0]
	if command.Action != agentlink.EUICCNotificationDeliver || command.Expected == nil ||
		command.Expected.SequenceNumber != 7 || command.Expected.Event != "enable" ||
		command.Expected.ICCID != testICCID || command.Expected.Address != "notify.example.com" ||
		!strings.HasPrefix(command.OperationID, "notification-") {
		t.Fatalf("command=%+v", command)
	}

	agents.notificationResult = agentlink.EUICCNotificationResponse{
		SessionGeneration: "insertion-a", EID: testEID, Acknowledged: true, Removed: false,
	}
	agents.notificationErr = &agentlink.RemoteError{
		Kind: "failed", Code: "euicc_notification_acknowledged_not_removed",
	}
	response = post(t, mux, path, map[string]any{
		"confirmed": true, "event": "enable", "iccid": testICCID, "address": "notify.example.com",
	})
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"acknowledged":true`) ||
		!strings.Contains(response.Body.String(), `"removed":false`) {
		t.Fatalf("partial status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDownloadSafetyRejectsRunningOrCallingMatchingLine(t *testing.T) {
	agents := &fakeAgents{statuses: []agentlink.ConnectionStatus{{
		Topology: topology("reader-a", "insertion-a", testEID,
			[]agentlink.EUICCProfileFact{{ICCID: testICCID, State: agentlink.EUICCProfileEnabled}}),
	}}}
	catalog := fakeCatalog{snapshot: linecatalog.Snapshot{Lines: []linecatalog.Line{{
		ID: "line-a", Enabled: true, CardID: testICCID,
	}}}}
	provider := fakeProviderStatus{statuses: map[string]vowifiipc.Snapshot{"line-a": {
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning},
	}}}
	service, err := New(agents, WithDownloadSafety(catalog, provider))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/euiccs/{eid}/downloads", service)
	payload := map[string]any{
		"operation_id": "download-safe-1", "activation_code": "LPA:1$example.com$matching-id",
		"imei": "123456789012345",
	}
	response := post(t, mux, "/v1/euiccs/"+testEID+"/downloads", payload)
	if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"euicc_download_line_active\"}\n" ||
		len(agents.downloadCommands) != 0 {
		t.Fatalf("running status=%d body=%s commands=%+v", response.Code, response.Body.String(), agents.downloadCommands)
	}
	provider.statuses["line-a"] = vowifiipc.Snapshot{
		Runtime:    vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeStopped},
		ActiveCall: &vowifiipc.ActiveCall{CallID: "call-a", Condition: vowifiipc.CallActive},
	}
	service.providers = provider
	payload["operation_id"] = "download-safe-2"
	response = post(t, mux, "/v1/euiccs/"+testEID+"/downloads", payload)
	if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"euicc_download_call_active\"}\n" ||
		len(agents.downloadCommands) != 0 {
		t.Fatalf("call status=%d body=%s commands=%+v", response.Code, response.Body.String(), agents.downloadCommands)
	}
}

func topology(reader, generation, eid string, profiles []agentlink.EUICCProfileFact) *agentlink.TopologySnapshot {
	return &agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{{
		ReaderName: reader, CardPresent: true, SessionGeneration: generation, IdentityState: agentlink.CardIdentified,
		EUICC: &agentlink.EUICCFact{EID: eid, ProfilesAvailable: true, ProfileManagement: true, ProfileDownload: true, ProfileDiscovery: true, NotificationInventory: true, NotificationDelivery: true, Profiles: profiles},
	}}}
}

func post(t *testing.T, handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(value)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
