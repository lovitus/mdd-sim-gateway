package egressdesired

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func desiredInput() (egressconfig.Snapshot, linecatalog.Snapshot, json.RawMessage) {
	config := egressconfig.Config{SchemaVersion: egressconfig.SchemaVersion, Enabled: true, MissingPolicy: "error",
		RefreshMinutes: 30, Profiles: map[string]egressconfig.Profile{
			"node-hk": {Name: "Hong Kong", Type: "node", Value: "ss://secret"},
		}, Exits: map[string]egressconfig.Exit{"hk": {Enabled: true, ProfileID: "node-hk"}}}
	catalog := linecatalog.Snapshot{SchemaVersion: linecatalog.SchemaVersion, Revision: 8, Lines: []linecatalog.Line{
		{SchemaVersion: 1, ID: "4", Name: "Hong Kong 00", Enabled: true, CardID: "8985200000000000001",
			SIM:     linecatalog.SIMConfig{IMSI: "454000000000001", MCC: "454", MNC: "00"},
			Network: linecatalog.NetworkConfig{EgressCountry: "hk"}},
		{SchemaVersion: 1, ID: "8", Name: "Disabled", Enabled: false, CardID: "1111"},
	}}
	return egressconfig.Snapshot{SchemaVersion: egressconfig.SchemaVersion, Revision: 5, Config: config},
		catalog, json.RawMessage(`{"auto_detect":true,"modem_profiles":{"keep":{"port":"COM1"}}}`)
}

func TestRenderV2ExcludesLegacyHardwareAndCatalogDetails(t *testing.T) {
	config, catalog, _ := desiredInput()
	first, err := Render(config, catalog, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(config, catalog, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != second.Generation || first.UpdatedAt == second.UpdatedAt {
		t.Fatalf("generation changed with time: first=%+v second=%+v", first, second)
	}
	if first.Version != 2 || len(first.Hardware) != 0 || len(first.Lines) != 0 {
		t.Fatalf("v2 document retained legacy orchestration: %+v", first)
	}
	catalog.Revision++
	catalogOnly, err := Render(config, catalog, time.Unix(300, 0))
	if err != nil || catalogOnly.Generation != first.Generation || catalogOnly.CatalogRevision == first.CatalogRevision {
		t.Fatalf("catalog-only change reloaded exit generation: first=%+v next=%+v err=%v", first, catalogOnly, err)
	}
}

func TestPublishSameGenerationDoesNotReplaceOrTouchFile(t *testing.T) {
	config, catalog, _ := desiredInput()
	document, err := Render(config, catalog, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "desired.json")
	changed, err := Publish(path, document)
	if err != nil || !changed {
		t.Fatalf("first publish changed=%v err=%v", changed, err)
	}
	oldTime := time.Unix(10, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	changed, err = Publish(path, document)
	if err != nil || changed {
		t.Fatalf("no-op publish changed=%v err=%v", changed, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !after.ModTime().Equal(oldTime) {
		t.Fatalf("no-op publish replaced or touched file: before=%v after=%v", before, after)
	}

	config.Revision++
	profile := config.Config.Profiles["node-hk"]
	profile.Name = "Hong Kong replacement"
	config.Config.Profiles["node-hk"] = profile
	replacement, err := Render(config, catalog, time.Unix(300, 0))
	if err != nil {
		t.Fatal(err)
	}
	changed, err = Publish(path, replacement)
	if err != nil || !changed {
		t.Fatalf("replacement publish changed=%v err=%v", changed, err)
	}
	newInfo, _ := os.Stat(path)
	if os.SameFile(after, newInfo) || replacement.Generation == document.Generation {
		t.Fatal("changed generation did not atomically replace the desired file")
	}
	applied, preserved, err := CurrentApplied(path)
	if err != nil || applied.ConfigRevision != config.Revision || applied.CatalogRevision != catalog.Revision ||
		applied.Generation != replacement.Generation || len(preserved) != 0 {
		t.Fatalf("current applied=%+v legacy=%s err=%v", applied, preserved, err)
	}
}

func TestPublishOwnedSetsExecutorIdentityBeforePublication(t *testing.T) {
	config, catalog, _ := desiredInput()
	document, err := Render(config, catalog, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "desired.json")
	changed, err := PublishOwned(path, document, os.Getuid(), os.Getgid(), 0o640)
	if err != nil || !changed {
		t.Fatalf("publish owned changed=%v err=%v", changed, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("owned desired mode=%v err=%v", info.Mode(), err)
	}
}

func TestWaitForRuntimeRequiresTheExactGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	wanted := strings.Repeat("a", 64)
	old := strings.Repeat("b", 64)
	if err := os.WriteFile(path, []byte(`{"desired_generation":"`+old+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err := WaitForRuntime(context.Background(), path, wanted, 25*time.Millisecond, 5*time.Millisecond)
	if !errors.Is(err, ErrRuntimeConfirmationTimeout) || time.Since(started) < 20*time.Millisecond {
		t.Fatalf("unconfirmed generation err=%v elapsed=%v", err, time.Since(started))
	}
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = os.WriteFile(path, []byte(`{"desired_generation":"`+wanted+`"}`), 0o600)
	}()
	if err := WaitForRuntime(context.Background(), path, wanted, time.Second, 5*time.Millisecond); err != nil {
		t.Fatalf("exact generation was not confirmed: %v", err)
	}
}
