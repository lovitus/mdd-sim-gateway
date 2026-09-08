package systempreferences

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAuditPreferencesNormalizeAndPreserveIndependentFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	current, err := store.Snapshot()
	if err != nil || current.Preferences.AuditEnabled == nil || !*current.Preferences.AuditEnabled {
		t.Fatal(current, err)
	}
	if same, err := store.PutExpected(current.Preferences, current.Revision); err != nil || same.Revision != current.Revision {
		t.Fatal("default no-op changed revision", same, err)
	}
	disabled := false
	current.Preferences.AuditEnabled = &disabled
	current.Preferences.TrustedProxies = []string{"192.0.2.12/24", "2001:db8::1", "127.0.0.1"}
	saved, err := store.PutExpected(current.Preferences, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Preferences.TrustedProxies[0] != "192.0.2.0/24" || saved.Preferences.TrustedProxies[1] != "2001:db8::1/128" || saved.Preferences.TrustedProxies[2] != "127.0.0.1/32" {
		t.Fatal(saved)
	}
	if saved.Preferences.CallAudioBufferMS != DefaultCallAudioBufferMS || saved.Preferences.RingTimeoutSeconds != DefaultRingTimeoutSeconds {
		t.Fatal("audit save changed voice settings")
	}
	invalid := saved.Preferences
	invalid.TrustedProxies = []string{"https://user:secret@example.invalid"}
	if _, err := store.PutExpected(invalid, saved.Revision); err == nil {
		t.Fatal("URL accepted as trusted IP")
	}
}
