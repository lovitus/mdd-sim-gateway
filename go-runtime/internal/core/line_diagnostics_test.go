package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

func TestLineDiagnosticsRequireCatalogIdentityAndReturnRedactedExport(t *testing.T) {
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	line := linecatalog.Line{SchemaVersion: 1, ID: "line-1", Name: "Line", Enabled: true,
		CardID: "8944100000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}}
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	store, err := events.OpenBoltStore(filepath.Join(t.TempDir(), "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	_, err = store.Activate(events.Event{SchemaVersion: 1, EventID: "diagnostic-event", LineID: line.ID,
		ProducerRole: events.RoleAgent, ProducerID: "agent-private", Layer: state.LayerHardware,
		Condition: state.ConditionDegraded, Code: "probe_failed",
		Detail: "imsi=234100000000001 peer=+441234567890", Generation: "generation-private",
		Sequence: 1, ObservedAt: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(testReplay(t, now), func() time.Time { return now },
		WithLineCatalog(catalog, nil), WithLineDiagnostics(store))
	request := httptest.NewRequest(http.MethodGet, "/v1/diagnostics/lines/line-1/logs/export?limit=10", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Disposition"), "redacted.json") {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var payload struct {
		LineID   string                   `json:"line_id"`
		Redacted bool                     `json:"redacted"`
		Entries  []events.DiagnosticEntry `json:"entries"`
	}
	if json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.LineID != line.ID || !payload.Redacted ||
		len(payload.Entries) != 1 || payload.Entries[0].Source != "agent" {
		t.Fatalf("payload=%+v", payload)
	}
	for _, secret := range []string{"234100000000001", "+441234567890", "agent-private", "generation-private"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("response leaked %q", secret)
		}
	}
	missing := httptest.NewRecorder()
	server.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/diagnostics/lines/missing/logs", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
	invalid := httptest.NewRecorder()
	server.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/v1/diagnostics/lines/line-1/logs?limit=501", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	unknown := httptest.NewRecorder()
	server.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/v1/diagnostics/lines/line-1/logs?tail=10", nil))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown query status=%d body=%s", unknown.Code, unknown.Body.String())
	}
}
