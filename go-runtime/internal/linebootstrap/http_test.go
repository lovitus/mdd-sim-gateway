package linebootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func request(handler http.Handler, method, path, candidateID, ifMatch, body string) *httptest.ResponseRecorder {
	value := httptest.NewRequest(method, path, strings.NewReader(body))
	if candidateID != "" {
		value.SetPathValue("candidateID", candidateID)
	}
	if ifMatch != "" {
		value.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, value)
	return response
}

func TestHTTPClaimRequiresRevisionAndRejectsForgedRawFields(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := testCatalog(t)
	facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{
		modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "89010000000000000001", "session-a"),
	}}
	service, _ := New(store, facts, func() time.Time { return now })
	handler, _ := NewHandler(service)

	listed := request(handler, http.MethodGet, "/v1/line-candidates", "", "", "")
	if listed.Code != http.StatusOK || listed.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET status=%d etag=%q body=%s", listed.Code, listed.Header().Get("ETag"), listed.Body.String())
	}
	var snapshot Snapshot
	if err := json.Unmarshal(listed.Body.Bytes(), &snapshot); err != nil || len(snapshot.Candidates) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	id := snapshot.Candidates[0].CandidateID
	missing := request(handler, http.MethodPost, "/v1/line-candidates/"+id+"/claim", id, "",
		`{"schema_version":1,"name":"draft"}`)
	if missing.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing revision status=%d body=%s", missing.Code, missing.Body.String())
	}
	forged := request(handler, http.MethodPost, "/v1/line-candidates/"+id+"/claim", id, `"1"`,
		`{"schema_version":1,"name":"draft","mode":"raw","raw_isolation_proved":true}`)
	if forged.Code != http.StatusBadRequest || !strings.Contains(forged.Body.String(), "invalid_line_claim") {
		t.Fatalf("forged status=%d body=%s", forged.Code, forged.Body.String())
	}
	stored, _ := store.Snapshot()
	if stored.Revision != 1 || len(stored.Lines) != 0 {
		t.Fatalf("forged request changed catalog: %+v", stored)
	}
}

func TestHTTPClaimCreatesOneDraftAndConcurrentRevisionPreventsSecond(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := testCatalog(t)
	facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{
		modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "89010000000000000001", "session-a"),
	}}
	service, _ := New(store, facts, func() time.Time { return now })
	handler, _ := NewHandler(service)
	snapshot, _ := service.Project()
	id := snapshot.Candidates[0].CandidateID
	body := `{"schema_version":1,"name":"new line"}`
	created := request(handler, http.MethodPost, "/v1/line-candidates/"+id+"/claim", id, `"1"`, body)
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"2"` {
		t.Fatalf("create status=%d etag=%q body=%s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	second := request(handler, http.MethodPost, "/v1/line-candidates/"+id+"/claim", id, `"1"`, body)
	if second.Code != http.StatusPreconditionFailed || second.Header().Get("ETag") != `"2"` {
		t.Fatalf("second status=%d etag=%q body=%s", second.Code, second.Header().Get("ETag"), second.Body.String())
	}
	stored, _ := store.Snapshot()
	if stored.Revision != 2 || len(stored.Lines) != 1 || stored.Lines[0].Enabled ||
		stored.Lines[0].HardwareProvisionState != "draft" {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestHTTPClaimRejectsAmbiguousICCIDFromIncompleteDuplicate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := testCatalog(t)
	complete := modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "89010000000000000001", "session-a")
	incomplete := modemStatus(now, "agent-b", "process-b", "attachment-b", "", "89010000000000000001", "")
	incomplete.Topology.Modems[0].AT = agentlink.ModemATControlFact{State: "unknown"}
	incomplete.Topology.Modems[0].Capabilities = agentlink.ModemCapabilities{}
	facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{complete, incomplete}}
	requireValidTopologies(t, facts.statuses)
	service, _ := New(store, facts, func() time.Time { return now })
	handler, _ := NewHandler(service)
	snapshot, _ := service.Project()

	response := request(handler, http.MethodPost, "/v1/line-candidates/"+snapshot.Candidates[0].CandidateID+"/claim",
		snapshot.Candidates[0].CandidateID, `"1"`, `{"schema_version":1,"name":"must not be created"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "card_identity_ambiguous") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	stored, err := store.Snapshot()
	if err != nil || stored.Revision != 1 || len(stored.Lines) != 0 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}
