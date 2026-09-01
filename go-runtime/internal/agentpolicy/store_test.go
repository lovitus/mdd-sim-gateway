package agentpolicy

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreKeysPolicyAndSecretsByExactEquipmentAndCard(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "policies.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	equipment, card := "862547055201716", "8985200000000000001"
	policy, found, err := store.Get(equipment, card)
	if err != nil || found || policy.Revision != 0 || policy.Desired.CellularEnabled || policy.Desired.FlightMode || policy.Desired.RoamingEnabled {
		t.Fatalf("default policy=%+v found=%t err=%v", policy, found, err)
	}
	policy.Desired.CellularEnabled, policy.Desired.RoamingEnabled = true, true
	policy, err = store.PutExpected(policy, 0)
	if err != nil || policy.Revision != 1 {
		t.Fatalf("put=%+v err=%v", policy, err)
	}
	if _, err := store.PutExpected(policy, 0); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale put err=%v", err)
	}
	profile := Profile{Name: "carrier", APN: "internet", Auth: "PAP", Username: "user", Password: "secret"}
	policy, err = store.SaveProfileExpected(equipment, card, profile, false, 1)
	if err != nil || policy.Revision != 2 || policy.Desired.SelectedProfile != "carrier" {
		t.Fatalf("save=%+v err=%v", policy, err)
	}
	loaded, ok, err := store.Profile(equipment, card, "carrier")
	if err != nil || !ok || loaded.Password != "secret" {
		t.Fatalf("profile=%+v ok=%t err=%v", loaded, ok, err)
	}
	profile.Password = ""
	if _, err := store.SaveProfileExpected(equipment, card, profile, true, 2); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ = store.Profile(equipment, card, "carrier")
	if loaded.Password != "secret" {
		t.Fatal("keep-password update cleared stored secret")
	}
	other, found, err := store.Get(equipment, "8985200000000000002")
	if err != nil || found || other.Desired.CellularEnabled || other.Desired.SelectedProfile != "" {
		t.Fatalf("new card inherited old policy: %+v found=%t err=%v", other, found, err)
	}
}
