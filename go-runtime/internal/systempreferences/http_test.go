package systempreferences

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestPreferencesPersistWithCASAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/system/preferences", nil))
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"1"` {
		t.Fatalf("initial GET status=%d headers=%v body=%s", get.Code, get.Header(), get.Body.String())
	}
	var initial Snapshot
	if json.Unmarshal(get.Body.Bytes(), &initial) != nil || initial.Preferences.CallAudioBufferMS != 500 {
		t.Fatalf("initial=%+v", initial)
	}

	patch := httptest.NewRequest(http.MethodPatch, "/v1/system/preferences", bytes.NewBufferString(`{"call_audio_buffer_ms":1500}`))
	patch.Header.Set("If-Match", `"1"`)
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, patch)
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"2"` {
		t.Fatalf("PATCH status=%d headers=%v body=%s", updated.Code, updated.Header(), updated.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Snapshot()
	if err != nil || persisted.Revision != 2 || persisted.Preferences.CallAudioBufferMS != 1500 {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}

	staleHandler, _ := NewHandler(reopened)
	staleRequest := httptest.NewRequest(http.MethodPatch, "/v1/system/preferences", bytes.NewBufferString(`{"call_audio_buffer_ms":1000}`))
	staleRequest.Header.Set("If-Match", `"1"`)
	stale := httptest.NewRecorder()
	staleHandler.ServeHTTP(stale, staleRequest)
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale PATCH status=%d body=%s", stale.Code, stale.Body.String())
	}
	badRequest := httptest.NewRequest(http.MethodPatch, "/v1/system/preferences", bytes.NewBufferString(`{"call_audio_buffer_ms":2001}`))
	badRequest.Header.Set("If-Match", `"2"`)
	bad := httptest.NewRecorder()
	staleHandler.ServeHTTP(bad, badRequest)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad PATCH status=%d body=%s", bad.Code, bad.Body.String())
	}
}
