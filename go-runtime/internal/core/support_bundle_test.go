package core

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
)

func TestSupportBundleContainsOnlyRedactedProjections(t *testing.T) {
	replay, err := events.NewReplay(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{now: time.Now, replay: replay}
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
	for _, entry := range reader.File {
		seen[entry.Name] = true
	}
	for _, name := range []string{"runtime.json", "diagnostics.json", "devices.json", "catalog.json"} {
		if !seen[name] {
			t.Fatalf("bundle missing %s", name)
		}
	}
	if bytes.Contains(response.Body.Bytes(), []byte("password")) || bytes.Contains(response.Body.Bytes(), []byte("token")) {
		t.Fatal("bundle contains a forbidden credential field")
	}
}
