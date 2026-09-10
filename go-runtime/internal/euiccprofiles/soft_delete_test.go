package euiccprofiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestArchivedReplayNeedsAllConfirmationsAndNeverResendsOperation(t *testing.T) {
	store, err := events.OpenBoltStore(filepath.Join(t.TempDir(), "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := agentlink.EUICCNotificationEntry{ICCID: testICCID, Event: "delete", SequenceNumber: 0, Address: "notify.example.com"}
	archive, err := store.SaveEUICCNotification(testEID, entry, []byte("fixture-only"))
	if err != nil {
		t.Fatal(err)
	}
	agents := &fakeAgents{notificationResult: agentlink.EUICCNotificationResponse{Acknowledged: true}, notificationErr: errors.New("card transaction release failed after ACK")}
	service, err := New(agents, WithDeletionStore(store))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/euiccs/{eid}/notifications/{sequence}/replay", service)
	request := map[string]any{"operation_id": "replay-test", "archive_sha256": archive.SHA256, "confirm_iccid": testICCID, "confirm_operator_deactivation": true, "confirm_retain_notification": true}
	post := func(body map[string]any) int {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/v1/euiccs/"+testEID+"/notifications/0/replay", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	for _, field := range []string{"confirm_iccid", "confirm_operator_deactivation", "confirm_retain_notification", "archive_sha256"} {
		copy := map[string]any{}
		for k, v := range request {
			copy[k] = v
		}
		delete(copy, field)
		if post(copy) < 400 {
			t.Fatalf("missing %s accepted", field)
		}
	}
	if len(agents.notificationCommands) != 0 {
		t.Fatal("unconfirmed replay sent")
	}
	if post(request) != 200 || post(request) != 200 || len(agents.notificationCommands) != 1 {
		t.Fatal("duplicate replay dispatched")
	}
	if agents.notificationCommands[0].Action != agentlink.EUICCNotificationReplay {
		t.Fatal("used destructive delivery action")
	}
	got, err := store.EUICCNotificationArchive(testEID, 0)
	if err != nil || len(got.Payload) == 0 || got.Attempts[0].State != "acknowledged" {
		t.Fatal("ack removed archive")
	}
}

func TestSoftDeleteAfterManualRenameRetainsBothEvents(t *testing.T) {
	store, err := events.OpenBoltStore(filepath.Join(t.TempDir(), "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	topo := topology("reader", "generation", testEID, []agentlink.EUICCProfileFact{{ICCID: testICCID, State: agentlink.EUICCProfileDisabled, Nickname: "before"}})
	topo.Readers[0].EUICC.SoftDelete = true
	agents := &fakeAgents{statuses: []agentlink.ConnectionStatus{{AgentID: "agent", Topology: topo}}, result: agentlink.EUICCProfileResponse{Outcome: agentlink.EUICCProfileRefreshPending, Changed: true}}
	service, err := New(agents, WithDeletionStore(store))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/euiccs/{eid}/profiles/{iccid}/soft-delete", service)
	route := "/v1/euiccs/" + testEID + "/profiles/" + testICCID + "/soft-delete"
	request := func(id, nickname string) map[string]any {
		return map[string]any{"operation_id": id, "expected_nickname": nickname, "confirm_iccid": testICCID, "confirm_keep_profile": true, "confirm_block_enable": true}
	}
	if w := post(t, mux, route, request("first", "before")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	topo.Readers[0].EUICC.Profiles[0].Nickname = "[MDD-DELETED] before"
	if w := post(t, mux, route, request("first", "before")); w.Code != 200 || len(agents.commands) != 1 {
		t.Fatal("duplicate changed card")
	}
	if w := post(t, mux, route, request("second", "before")); w.Code != 409 || len(agents.commands) != 1 {
		t.Fatal("stale page created another event")
	}
	topo.Readers[0].EUICC.Profiles[0].Nickname = "manually restored"
	if w := post(t, mux, route, request("second", "manually restored")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	history, err := store.EUICCSoftDeleteHistory(testEID, testICCID)
	if err != nil || len(history) != 2 || history[0].OperationID != "first" || history[1].OperationID != "second" || len(agents.commands) != 2 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}
