package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func TestAvailabilityRouteUsesCatalogAndReportsNoEvidenceAsUnknown(t *testing.T) {
	now := time.Unix(1800100000, 0).UTC()
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	if _, err := catalog.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Name: "Line", CardID: "8944100000000000001",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}}); err != nil {
		t.Fatal(err)
	}
	store, err := events.OpenBoltStore(filepath.Join(t.TempDir(), "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	options := []Option{WithLineCatalog(catalog, nil), WithLineDiagnostics(store)}
	server := NewServer(testReplay(t, now), func() time.Time { return now }, options...)
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/v1/lines/line-1/availability", 200},
		{"/v1/lines/missing/availability", 404},
		{"/v1/lines/line-1/availability?span=999999999", 400},
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, response.Code, response.Body.String())
		}
		if test.status == 200 {
			var history events.Availability
			if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
				t.Fatal(err)
			}
			if history.RecordedSince != nil || history.Summary.Ratio != nil || history.Summary.Unknown != history.Span {
				t.Fatalf("invented history: %+v", history)
			}
		}
	}
	options = append(options, WithManagementAuth(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	}))
	server = NewServer(testReplay(t, now), func() time.Time { return now }, options...)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/lines/line-1/availability", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatal(response.Code)
	}
}
