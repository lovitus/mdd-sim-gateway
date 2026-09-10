package euiccprofiles

import (
	"bytes"
	"encoding/json"
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
	agents := &fakeAgents{notificationResult: agentlink.EUICCNotificationResponse{Acknowledged: true}}
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
