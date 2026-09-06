package core

import (
	"archive/zip"
	"bytes"
	"io"
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

func TestSupportBundleContainsOnlyRedactedProjections(t *testing.T) {
	replay, err := events.NewReplay(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	line := linecatalog.Line{SchemaVersion: 1, ID: "line-1", Name: "Private name", Enabled: false,
		CardID: "8944100000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10",
			MSISDN: "+441234567890"}}
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	eventStore, err := events.OpenBoltStore(filepath.Join(t.TempDir(), "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer eventStore.Close()
	now := time.Now().UTC()
	if _, err := eventStore.Activate(events.Event{SchemaVersion: 1, EventID: "support-event", LineID: line.ID,
		ProducerRole: events.RoleAgent, ProducerID: "agent-private", Layer: state.LayerHardware,
		Condition: state.ConditionDegraded, Code: "probe_failed", Detail: "imsi=234100000000001 peer=+441234567890 ip=192.0.2.10",
		Generation: "generation-private", Sequence: 1, ObservedAt: now}, now); err != nil {
		t.Fatal(err)
	}
	server := &Server{now: time.Now, replay: replay, catalog: catalog, eventStore: eventStore,
		runtimeInfo: &RuntimeInfo{Public: PublicRuntimeInfo{TLSFingerprintSHA256: "fingerprint-private"}}}
	request := httptest.NewRequest(http.MethodGet, "/v1/diagnostics/support-bundle", nil)
	response := httptest.NewRecorder()
	server.supportBundle(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("status=%d content-type=%q", response.Code, response.Header().Get("Content-Type"))
	}
	reader, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	var expanded strings.Builder
	for _, entry := range reader.File {
		seen[entry.Name] = true
		file, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		expanded.Write(payload)
	}
	for _, name := range []string{"runtime.json", "diagnostics.json", "devices.json", "catalog.json", "line-logs.json"} {
		if !seen[name] {
			t.Fatalf("bundle missing %s", name)
		}
	}
	for _, secret := range []string{"8944100000000000001", "234100000000001", "+441234567890", "192.0.2.10",
		"Private name", "agent-private", "generation-private", "fingerprint-private"} {
		if strings.Contains(expanded.String(), secret) {
			t.Fatalf("expanded bundle contains %q", secret)
		}
	}
	for _, forbidden := range []string{`"card_id"`, `"imsi"`, `"msisdn"`, `"process_generation"`} {
		if strings.Contains(expanded.String(), forbidden) {
			t.Fatalf("expanded bundle contains forbidden field %s", forbidden)
		}
	}
}
