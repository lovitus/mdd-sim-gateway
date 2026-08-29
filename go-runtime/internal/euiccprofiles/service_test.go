package euiccprofiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	statuses         []agentlink.ConnectionStatus
	commands         []agentlink.EUICCProfileCommand
	result           agentlink.EUICCProfileResponse
	err              error
	downloadCommands []agentlink.EUICCDownloadCommand
	downloadResult   agentlink.EUICCDownloadResponse
	downloadErr      error
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
		EUICC: &agentlink.EUICCFact{EID: eid, ProfilesAvailable: true, ProfileManagement: true, ProfileDownload: true, Profiles: profiles},
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
