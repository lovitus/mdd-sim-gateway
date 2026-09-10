package euiccprofiles

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryCodesRequireExactExplicitReveal(t *testing.T) {
	store, err := events.OpenBoltStore(filepath.Join(t.TempDir(), "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	command := agentlink.EUICCDownloadCommand{EID: testEID, OperationID: "download-recovery", Action: agentlink.EUICCDownloadStart, ActivationCode: "LPA:1$example.com$private-activation", ConfirmationCode: "private-confirmation"}
	if err := store.SaveEUICCDownloadRecovery(command, true); err != nil {
		t.Fatal(err)
	}
	if err := store.BindEUICCDownloadRecovery(testEID, command.OperationID, agentlink.EUICCDownloadMetadata{ICCID: testICCID, ProfileName: "Original profile", ServiceProviderName: "Original provider"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginEUICCDeletion(events.EUICCDeletion{EID: testEID, ICCID: testICCID, OperationID: "delete-record", DownloadOperationID: command.OperationID}); err != nil {
		t.Fatal(err)
	}
	service, err := New(&fakeAgents{}, WithDeletionStore(store))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/euiccs/{eid}/deletions", service)
	mux.Handle("POST /v1/euiccs/{eid}/profiles/{iccid}/recovery-codes", service)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/euiccs/"+testEID+"/deletions", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-") || !strings.Contains(w.Body.String(), "Original profile") {
		t.Fatal("public metadata missing or secret leaked", w.Code)
	}
	route := "/v1/euiccs/" + testEID + "/profiles/" + testICCID + "/recovery-codes"
	if result := post(t, mux, route, map[string]any{"confirmed": false, "confirm_iccid": testICCID, "download_operation_id": command.OperationID}); result.Code != 400 {
		t.Fatal("unconfirmed reveal accepted")
	}
	if result := post(t, mux, route, map[string]any{"confirmed": true, "confirm_iccid": "other", "download_operation_id": command.OperationID}); result.Code != 400 {
		t.Fatal("wrong identity accepted")
	}
	result := post(t, mux, route, map[string]any{"confirmed": true, "confirm_iccid": testICCID, "download_operation_id": command.OperationID})
	if result.Code != 200 || !strings.Contains(result.Body.String(), "private-confirmation") || result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("explicit reveal failed")
	}
}
