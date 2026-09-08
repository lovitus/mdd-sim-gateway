package systempreferences

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestNewDeviceDefaultsPreserveOriginalBaselineAndExplicitChoices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	initial, err := store.Snapshot()
	if err != nil || initial.Preferences.NewDeviceDefaults != nil {
		t.Fatal("unconfigured defaults were invented", err)
	}
	handler, _ := NewHandler(store)
	patch := func(body, revision string, want int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPatch, "/v1/system/preferences", bytes.NewBufferString(body))
		request.Header.Set("If-Match", revision)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
		}
	}
	patch(`{"new_device_defaults":{}}`, `"1"`, http.StatusBadRequest)
	patch(`{"new_device_defaults":{"cellular_enabled":true}}`, `"1"`, http.StatusBadRequest)
	patch(`{"new_device_defaults":{"connection_enabled":false}}`, `"1"`, http.StatusOK)
	first, _ := store.Snapshot()
	if first.Preferences.NewDeviceDefaults == nil || *first.Preferences.NewDeviceDefaults != (NewDeviceDefaults{VoWiFiEnabled: true}) {
		t.Fatal("first explicit default edit lost the original VoWiFi baseline")
	}
	patch(`{"new_device_defaults":{"vowifi_enabled":true,"roaming_enabled":true},"call_audio_buffer_ms":501}`, `"2"`, http.StatusOK)
	patch(`{"new_device_defaults":{"flight_mode":true}}`, `"2"`, http.StatusPreconditionFailed)
	patch(`{"new_device_defaults":{"roaming_enabled":false}}`, `"3"`, http.StatusOK)
	patch(`{"ring_timeout_seconds":40}`, `"4"`, http.StatusOK)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	want := NewDeviceDefaults{VoWiFiEnabled: true}
	if latest.Preferences.NewDeviceDefaults == nil || *latest.Preferences.NewDeviceDefaults != want || latest.Revision != 5 ||
		latest.Preferences.CallAudioBufferMS != 501 || latest.Preferences.RingTimeoutSeconds != 40 {
		t.Fatal("partial update, stale writer or reopen lost settings")
	}
	handler, _ = NewHandler(store)
	patch(`{"new_device_defaults":{"vowifi_enabled":false}}`, `"5"`, http.StatusOK)
	patch(`{"new_device_defaults":{"flight_mode":true}}`, `"6"`, http.StatusOK)
	latest, err = store.Snapshot()
	if err != nil || latest.Preferences.NewDeviceDefaults == nil ||
		*latest.Preferences.NewDeviceDefaults != (NewDeviceDefaults{FlightMode: true}) {
		t.Fatal("partial edits reopened explicitly disabled VoWiFi", err)
	}
}
