package core

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestMobileStreamRequiresAuthAndRevokesSession(t *testing.T) {
	verifier := &toggleBrowserVerifier{}
	server := NewServer(testReplay(t, time.Now()), time.Now, WithBrowserControl(verifier))
	server.browserEvery = 10 * time.Millisecond
	host := httptest.NewServer(server)
	defer host.Close()
	url := "ws" + strings.TrimPrefix(host.URL, "http") + "/v1/mobile/ws"
	if conn, response, err := websocket.Dial(t.Context(), url, nil); err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		if conn != nil {
			conn.CloseNow()
		}
		t.Fatalf("unauthorized=%v %v", response, err)
	}
	verifier.allowed.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var first mobileEnvelope
	if err = wsjson.Read(ctx, conn, &first); err != nil {
		t.Fatal(err)
	}
	if first.Type != "mobile.snapshot" || first.Sequence != 1 || first.Data == nil || first.SchemaVersion != 1 {
		t.Fatalf("bad first snapshot: %+v", first)
	}
	verifier.allowed.Store(false)
	var next mobileEnvelope
	if err = wsjson.Read(ctx, conn, &next); websocket.CloseStatus(err) != browserAuthClose {
		t.Fatalf("revocation did not close: %v", err)
	}
}

func TestMobileReadinessDoesNotRepublishUnchangedHeartbeatMetadata(t *testing.T) {
	inputs := map[string]state.Readiness{"vowifi_call": {Ready: true, Facts: []state.FactView{{Layer: state.LayerIMS, Condition: state.ConditionReady, Available: true, Fresh: true, ObservedAt: time.Now(), ReceivedAt: time.Now(), Epoch: 1, Sequence: 1}}}}
	first, err := json.Marshal(compactMobileOperations(inputs))
	if err != nil {
		t.Fatal(err)
	}
	row := inputs["vowifi_call"]
	row.Facts[0].ObservedAt = time.Now().Add(time.Second)
	row.Facts[0].ReceivedAt = time.Now().Add(time.Second)
	row.Facts[0].Sequence = 200
	inputs["vowifi_call"] = row
	second, err := json.Marshal(compactMobileOperations(inputs))
	if err != nil || string(first) != string(second) {
		t.Fatal("idle heartbeat metadata caused mobile traffic", err)
	}
	row.Ready = false
	row.Facts[0].Fresh = false
	row.Facts[0].Code = "expired"
	inputs["vowifi_call"] = row
	third, err := json.Marshal(compactMobileOperations(inputs))
	if err != nil || string(first) == string(third) {
		t.Fatal("actual readiness expiry was hidden", err)
	}
}

type mobileObservedFacts []callhistory.CallObservation

func (f mobileObservedFacts) CurrentVoWiFiCalls(time.Time) []callhistory.CallObservation { return f }

func TestMobileUsesCurrentPushedFactsInsteadOfPollingEveryProvider(t *testing.T) {
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "lines.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	line := linecatalog.Line{ID: "line-1", Enabled: true, CardID: "8944100000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}}
	if _, err = catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	facts := mobileObservedFacts{{LineID: line.ID, CardID: line.CardID, Generation: "generation-1", Snapshot: vowifiipc.Snapshot{PendingIncomingCall: &vowifiipc.PendingIncomingCall{CallID: "incoming-pushed"}}}}
	server := NewServer(testReplay(t, time.Now()), time.Now, WithLineCatalog(catalog, nil), WithMobileCallFacts(facts), WithProviderFacts(fixedProviderFacts{"line-1": "generation-1"}))
	// No control handler: only authenticated pushed facts can supply this event.
	got := server.readMobileSnapshot(t.Context())
	if len(got.IncomingLines) != 1 || got.IncomingLines[0].Incoming.CallID != "incoming-pushed" {
		t.Fatalf("missing pushed event %+v", got)
	}
	facts[0].Generation = "old-generation"
	got = server.readMobileSnapshot(t.Context())
	if len(got.IncomingLines) != 0 {
		t.Fatal("retired generation presented an incoming call")
	}
}

type mobileDirectoryControl struct{}

func (mobileDirectoryControl) ServeHTTP(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
func (mobileDirectoryControl) Status(_ context.Context, id string) (vowifiipc.Snapshot, error) {
	result := vowifiipc.Snapshot{}
	if id == "line-128" {
		result.PendingIncomingCall = &vowifiipc.PendingIncomingCall{CallID: "incoming-last"}
	}
	return result, nil
}

func TestMobileIncomingCoverageIndependentOfDirectoryPage(t *testing.T) {
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "lines.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	for i := 0; i < 129; i++ {
		if _, err = catalog.Put(linecatalog.Line{ID: fmt.Sprintf("line-%03d", i), Enabled: true, CardID: fmt.Sprintf("894410000000000%04d", i), SIM: linecatalog.SIMConfig{IMSI: fmt.Sprintf("23410000000%04d", i), MCC: "234", MNC: "10", MSISDN: "+441234567890"}}); err != nil {
			t.Fatal(err)
		}
	}
	verifier := &toggleBrowserVerifier{}
	verifier.allowed.Store(true)
	server := NewServer(testReplay(t, time.Now()), time.Now, WithBrowserControl(verifier), WithLineCatalog(catalog, nil))
	server.control = mobileDirectoryControl{}
	snapshot := server.readMobileSnapshot(t.Context())
	if len(snapshot.Lines) != 128 || len(snapshot.IncomingLines) != 1 || snapshot.IncomingLines[0].ID != "line-128" {
		t.Fatalf("directory=%d incoming=%+v", len(snapshot.Lines), snapshot.IncomingLines)
	}
	for _, path := range []string{"/v1/mobile/lines?after=line-127", "/v1/mobile/lines?q=line-128"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		var result struct {
			Lines []mobileLine `json:"lines"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Lines) != 1 || result.Lines[0].ID != "line-128" {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest("GET", "/v1/mobile/lines/line-128", nil))
	var line mobileLine
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &line) != nil || line.Incoming == nil || line.Incoming.CallID != "incoming-last" {
		t.Fatalf("exact line %d %s", response.Code, response.Body.String())
	}
}
func TestMobileStreamRejectsCrossOrigin(t *testing.T) {
	verifier := &toggleBrowserVerifier{}
	verifier.allowed.Store(true)
	host := httptest.NewServer(NewServer(testReplay(t, time.Now()), time.Now, WithBrowserControl(verifier)))
	defer host.Close()
	conn, response, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(host.URL, "http")+"/v1/mobile/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://other.invalid"}}})
	if conn != nil {
		conn.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross origin=%v %v", response, err)
	}
}
