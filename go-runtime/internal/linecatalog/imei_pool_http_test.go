package linecatalog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIMEIPoolHTTPRequiresBothRevisionsAndReturnsMachineCodes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := imeiTestLine("line-a", "8944100000000000001")
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	handler := NewIMEIPoolHandler(store)
	mux := http.NewServeMux()
	mux.Handle("GET /v1/imei-pool", handler)
	mux.Handle("PUT /v1/imei-pool/{entryID}", handler)
	mux.Handle("PUT /v1/imei-pool/{entryID}/bindings/{lineID}", handler)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/imei-pool", nil))
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET status=%d ETag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	entry := IMEIPoolEntry{SchemaVersion: 1, ID: "device-a", Name: "Device A", IMEI: "862547055201716"}
	payload, _ := json.Marshal(entry)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/imei-pool/device-a", bytes.NewReader(payload)))
	if response.Code != http.StatusPreconditionRequired || !strings.Contains(response.Body.String(), "imei_pool_revision_required") {
		t.Fatalf("missing CAS status=%d body=%s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodPut, "/v1/imei-pool/device-a", bytes.NewReader(payload))
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("create status=%d ETag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	binding := []byte(`{"expected_catalog_revision":1,"expected_card_id":"8944100000000000001"}`)
	request = httptest.NewRequest(http.MethodPut, "/v1/imei-pool/device-a/bindings/line-a", bytes.NewReader(binding))
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed || !strings.Contains(response.Body.String(), "catalog_revision_changed") {
		t.Fatalf("stale catalog status=%d body=%s", response.Code, response.Body.String())
	}
	binding = []byte(`{"expected_catalog_revision":2,"expected_card_id":"8944100000000000001"}`)
	request = httptest.NewRequest(http.MethodPut, "/v1/imei-pool/device-a/bindings/line-a", bytes.NewReader(binding))
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` ||
		!strings.Contains(response.Body.String(), `"catalog_revision":3`) {
		t.Fatalf("bind status=%d ETag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
}
