package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func TestAgentPINCredentialsPersistByCASWithoutReturningSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	settings := testConfig(t)
	settings.Agent.PINs = map[string]string{}
	settings.Agent.PINRevisions = map[string]string{}
	if err := saveConfig(path, settings); err != nil {
		t.Fatal(err)
	}
	store, err := newAgentPINCredentials(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	const cardID, pin = "89010000000000000001", "2468"
	before, err := store.Configuration(t.Context(), cardID)
	if err != nil || before.Configured || before.Revision != "" {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	saved, err := store.Save(t.Context(), cardID, pin, before.Revision)
	if err != nil || !saved.Configured || saved.Revision == "" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if _, err := store.Save(t.Context(), cardID, "1357", ""); !errors.Is(err, agentlink.ErrSIMPINConfigurationChanged) {
		t.Fatalf("stale save error=%v", err)
	}
	loaded, err := loadConfig(path)
	if err != nil || loaded.Agent.PINs[cardID] != pin || loaded.Agent.PINRevisions[cardID] != saved.Revision {
		t.Fatalf("persisted configuration mismatch err=%v", err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("config permissions=%v err=%v", info, err)
		}
	}
	if _, err := store.Remove(t.Context(), cardID, "stale-revision"); !errors.Is(err, agentlink.ErrSIMPINConfigurationChanged) {
		t.Fatalf("stale remove error=%v", err)
	}
	removed, err := store.Remove(t.Context(), cardID, saved.Revision)
	if err != nil || removed.Configured || removed.Revision != "" {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	loaded, err = loadConfig(path)
	if err != nil || loaded.Agent.PINs[cardID] != "" || loaded.Agent.PINRevisions[cardID] != "" {
		t.Fatalf("removed PIN remained err=%v", err)
	}
}

func TestAgentPINCredentialsHonorCancellationBeforeWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	settings := testConfig(t)
	if err := saveConfig(path, settings); err != nil {
		t.Fatal(err)
	}
	store, _ := newAgentPINCredentials(path, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Save(ctx, "89010000000000000001", "2468", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled save error=%v", err)
	}
	loaded, _ := loadConfig(path)
	if len(loaded.Agent.PINs) != 0 {
		t.Fatal("canceled save changed configuration")
	}
}

func TestAgentPINCredentialsCanReplaceLegacyEntryWithoutRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	settings := testConfig(t)
	settings.Agent.PINs = map[string]string{"89010000000000000001": "2468"}
	if err := saveConfig(path, settings); err != nil {
		t.Fatal(err)
	}
	store, _ := newAgentPINCredentials(path, nil)
	status, err := store.Configuration(t.Context(), "89010000000000000001")
	if err != nil || !status.Configured || status.Revision != "legacy-config" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err := store.Save(t.Context(), "89010000000000000001", "1357", status.Revision); err != nil {
		t.Fatal(err)
	}
}
