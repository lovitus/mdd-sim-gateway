package linecatalog

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestIKEPeriodPersistsAndPartialDefaultSaveDoesNotResetOtherTimer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PutProviderDefaults(ProviderDefaults{RekeyMinutes: 30, IKERekeyMinutes: 600}, 1); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"rekey_minutes":0}`))
	req.Header.Set("If-Match", `"2"`)
	rec := httptest.NewRecorder()
	NewDefaultsHandler(store).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("save HTTP %d", rec.Code)
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
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Defaults.RekeyMinutes != 0 || snapshot.Defaults.IKERekeyMinutes != 600 {
		t.Fatal("one timer erased the other")
	}
	zero := 0
	line := Line{Network: NetworkConfig{IKERekeyMinutes: &zero}}
	if EffectiveIKERekeyMinutes(line, snapshot.Defaults) != 0 {
		t.Fatal("explicit IKE disable ignored")
	}
}
