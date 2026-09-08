package systempreferences

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyDeviceDefaultsPreserveOriginalChoices(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  NewDeviceDefaults
	}{
		{`{"defaults":{}}`, NewDeviceDefaults{VoWiFiEnabled: true}},
		{`{"defaults":{"cellular_enabled":true,"vowifi_enabled":false,"roaming_enabled":true},"devices":{"existing":{"vowifi_enabled":true}}}`, NewDeviceDefaults{ConnectionEnabled: true, RoamingEnabled: true}},
	} {
		got, err := ReadLegacyDefaults([]byte(tc.input))
		if err != nil || got != tc.want {
			t.Fatalf("defaults mismatch: %v", err)
		}
	}
	for _, input := range []string{`{}`, `{"defaults":null}`, `{"defaults":[]}`, `{"defaults":{"cellular_enabled":"false"}}`, `{"defaults":{}} {}`} {
		if _, err := ReadLegacyDefaults([]byte(input)); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	store, err := Open(filepath.Join(t.TempDir(), "preferences.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, _ := store.Snapshot()
	value := NewDeviceDefaults{VoWiFiEnabled: true}
	after, created, err := store.ImportLegacyDefaults(value)
	if err != nil || !created || after.Preferences.CallAudioBufferMS != before.Preferences.CallAudioBufferMS {
		t.Fatal("import changed unrelated preferences", err)
	}
	again, created, err := store.ImportLegacyDefaults(value)
	if err != nil || created || again.Revision != after.Revision {
		t.Fatal("same import changed revision", err)
	}
	if _, _, err := store.ImportLegacyDefaults(NewDeviceDefaults{}); err == nil {
		t.Fatal("existing choice overwritten")
	}
}
