package providerapply

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const (
	preflightToken = "preflight-local-token-32-bytes-long"
	providerToken  = "provider-loopback-token-32-bytes-long"
)

func TestPreflightReportsRealActiveCallAndAbsentProvider(t *testing.T) {
	status := testStatus()
	providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/status" || request.Header.Get("Authorization") != "Bearer "+providerToken {
			http.Error(response, "rejected", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(response).Encode(status)
	}))
	defer providerServer.Close()
	directory := mediaauth.NewProviderDirectory()
	if err := directory.Replace(mediaauth.Provider{
		LineID: "line-1", ProviderID: "native", Generation: status.ProcessGeneration,
		BaseURL: "ws" + strings.TrimPrefix(providerServer.URL, "http"), Token: providerToken,
	}); err != nil {
		t.Fatal(err)
	}
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	for _, line := range []linecatalog.Line{
		testLine("line-1", "8944100000000000001"), testLine("line-2", "8944100000000000002"),
	} {
		if _, err := catalog.Put(line); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := NewHandler(catalog, directory, preflightToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := handler.Snapshot(t.Context())
	if err != nil || snapshot.CatalogRevision != 2 || len(snapshot.Lines) != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.Lines[0].Code != "provider_reachable" || snapshot.Lines[0].ActiveCall == nil ||
		snapshot.Lines[0].ActiveCall.CallID != "call-1" || snapshot.Lines[1].Code != "provider_absent" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	request := httptest.NewRequest(http.MethodGet, Path, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Authorization", "Bearer "+preflightToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"call_id":"call-1"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, Path, nil)
	request.RemoteAddr = "192.0.2.10:12345"
	request.Header.Set("Authorization", "Bearer "+preflightToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d", response.Code)
	}
}

func testLine(id, cardID string) linecatalog.Line {
	return linecatalog.Line{
		ID: id, Enabled: true, CardID: cardID,
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"},
	}
}

func testStatus() vowifiipc.Snapshot {
	stopped := vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped, Code: "stopped"}
	return vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-1", ProviderID: "native",
		ProcessGeneration: "provider-generation-1", Sequence: 1, ObservedAt: time.Now().UTC(),
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning, Code: "running"},
		Tunnel:  stopped, IMS: stopped, Voice: stopped, Messaging: stopped,
		ActiveCall: &vowifiipc.ActiveCall{CallID: "call-1", Condition: vowifiipc.CallActive},
	}
}
