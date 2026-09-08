package systempreferences

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRetryWindowPersistsAndRejectsStaleOrInvalidUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler, _ := NewHandler(store)
	initial, err := store.Snapshot()
	if err != nil || initial.Preferences.Retry == nil || initial.Preferences.Retry.Max != 3 || initial.Preferences.Retry.Interval != 40 {
		t.Fatal("legacy retry defaults missing", err)
	}
	patch := func(body, revision string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch, "/v1/system/preferences", bytes.NewBufferString(body))
		r.Header.Set("If-Match", revision)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	patch(`{"retry":{"max":0,"interval":40}}`, `"1"`, http.StatusBadRequest)
	patch(`{"retry":{"max":3,"interval":4}}`, `"1"`, http.StatusBadRequest)
	patch(`{"retry":{"max":4,"interval":30}}`, `"1"`, http.StatusOK)
	patch(`{"retry":{"max":1,"interval":5}}`, `"1"`, http.StatusPreconditionFailed)
	patch(`{"call_audio_buffer_ms":501}`, `"2"`, http.StatusOK)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	if err != nil || snapshot.Revision != 3 || snapshot.Preferences.Retry.Max != 4 || snapshot.Preferences.Retry.Interval != 30 || snapshot.Preferences.CallAudioBufferMS != 501 {
		t.Fatal("retry save/reopen lost settings", err)
	}
}
