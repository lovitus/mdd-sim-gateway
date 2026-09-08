package systempreferences

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateNetworkPatchPreservesOtherSettings(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "preferences.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initial, err := store.Snapshot()
	if err != nil || initial.Preferences.Updates == nil || initial.Preferences.Updates.Mode != "direct" {
		t.Fatal(initial, err)
	}
	handler, _ := NewHandler(store)
	request := httptest.NewRequest(http.MethodPatch, "/v1/system/preferences", bytes.NewBufferString(`{"updates":{"proxy_mode":"library","proxy_profile_id":"proxy-a"}}`))
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	updated, err := store.Snapshot()
	if err != nil || updated.Preferences.Updates.ProfileID != "proxy-a" || updated.Preferences.CallAudioBufferMS != 500 || !*updated.Preferences.AuditEnabled {
		t.Fatal(updated, err)
	}
	request = httptest.NewRequest(http.MethodPatch, "/v1/system/preferences", bytes.NewBufferString(`{"updates":{"proxy_mode":"direct","proxy_profile_id":"proxy-a"}}`))
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatal("contradictory route selection accepted", response.Code)
	}
}
