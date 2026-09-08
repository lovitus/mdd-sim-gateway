package euiccprofiles

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshReturnsActualEmptyInventoryWithoutProfileWrite(t *testing.T) {
	agents := &fakeAgents{result: agentlink.EUICCProfileResponse{EID: testEID, Outcome: agentlink.EUICCProfileRefreshed,
		Inventory: &agentlink.EUICCFact{EID: testEID, ProfilesAvailable: true, Profiles: []agentlink.EUICCProfileFact{}}}}
	service, err := New(agents)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/euiccs/"+testEID+"/refresh", strings.NewReader(`{"operation_id":"read-1"}`))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("eid", testEID)
	w := httptest.NewRecorder()
	service.ServeHTTP(w, r)
	if w.Code != 200 || len(agents.commands) != 1 || agents.commands[0].Action != agentlink.EUICCProfileRefresh || agents.commands[0].ICCID != "" {
		t.Fatal(w.Code, w.Body.String(), agents.commands)
	}
}
