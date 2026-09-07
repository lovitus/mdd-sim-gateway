package linecatalog

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestProviderDefaultsPersistAndRespectExplicitLineOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.PutProviderDefaults(ProviderDefaults{RekeyMinutes: 30}, 1)
	if err != nil || revision != 2 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
	if _, err := store.PutProviderDefaults(ProviderDefaults{RekeyMinutes: 60}, 1); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale update=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot()
	if err != nil || snapshot.Defaults.RekeyMinutes != 30 || snapshot.Revision != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	line := Line{}
	if EffectiveRekeyMinutes(line, snapshot.Defaults) != 30 {
		t.Fatal("global default lost")
	}
	disabled := 0
	line.Network.RekeyMinutes = &disabled
	if EffectiveRekeyMinutes(line, snapshot.Defaults) != 0 {
		t.Fatal("explicit zero override lost")
	}
	if _, err := store.PutProviderDefaults(ProviderDefaults{RekeyMinutes: 1441}, 2); err == nil {
		t.Fatal("invalid period accepted")
	}
}
