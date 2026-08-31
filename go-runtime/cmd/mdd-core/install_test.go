package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
)

func TestParseProviderUnitsRequiresCanonicalManagedInstances(t *testing.T) {
	valid := "mdd-vowifi@.service linked\n" +
		"mdd-vowifi@line-0123456789abcdef0123456789abcdef.service enabled\n"
	units, err := parseProviderUnits(valid)
	if err != nil || len(units) != 1 || units[0] != "mdd-vowifi@line-0123456789abcdef0123456789abcdef.service" {
		t.Fatalf("units=%v err=%v", units, err)
	}
	for _, invalid := range []string{
		"mdd-vowifi@line-short.service enabled",
		"mdd-vowifi@line-0123456789ABCDEF0123456789ABCDEF.service enabled",
		"ssh.service enabled",
	} {
		if _, err := parseProviderUnits(invalid); err == nil {
			t.Fatalf("invalid systemd output accepted: %q", invalid)
		}
	}
}

func TestNonEnabledUnitStateDistinguishesEnablement(t *testing.T) {
	for _, state := range []string{"disabled", "static", "linked", "masked", "not-found"} {
		if !nonEnabledUnitState(state) {
			t.Fatalf("safe state rejected: %s", state)
		}
	}
	for _, state := range []string{"enabled", "enabled-runtime", "alias", "disabled extra", ""} {
		if nonEnabledUnitState(state) {
			t.Fatalf("enabled or malformed state accepted: %q", state)
		}
	}
}

func TestConfiguredProviderUnitsIncludesStrictCurrentManifest(t *testing.T) {
	current, want := writeRemovalProviderManifest(t, "provider-line-7")
	units, err := configuredProviderUnits(current)
	if err != nil || len(units) != 1 || units[0] != want {
		t.Fatalf("units=%v want=%q err=%v", units, want, err)
	}
	if err := os.Remove(filepath.Join(mustTestLink(t, current), providerconfig.UnitInstance("provider-line-7")+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredProviderUnits(current); err == nil {
		t.Fatal("provider manifest with a missing config was accepted")
	}
}

func writeRemovalProviderManifest(t *testing.T, lineID string) (string, string) {
	t.Helper()
	root := t.TempDir()
	providerDirectory := filepath.Join(root, "providers-revision")
	if err := os.Mkdir(providerDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	instance := providerconfig.UnitInstance(lineID)
	config := providerconfig.Config{LineID: lineID, ProviderID: "vowifi-go", DeviceID: "device-1"}
	config.IPC.Listen = "127.0.0.1:0"
	config.IPC.Token = strings.Repeat("i", 32)
	config.IPC.StatePath = "/var/lib/mdd/providers/test.json"
	config.Agent.BrokerToken = strings.Repeat("a", 32)
	configPayload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configName := instance + ".json"
	if err := os.WriteFile(filepath.Join(providerDirectory, configName), configPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(configPayload)
	manifest := providerconfig.Manifest{
		SchemaVersion: 1, CatalogRevision: 1,
		Providers: []providerconfig.ManifestEntry{{
			LineID: lineID, UnitInstance: instance, ConfigFile: configName,
			ConfigSHA256: hex.EncodeToString(digest[:]),
		}},
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(providerDirectory, "manifest.json"), manifestPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "providers-current")
	if err := os.Symlink(providerDirectory, current); err != nil {
		t.Fatal(err)
	}
	return current, "mdd-vowifi@" + instance + ".service"
}

func mustTestLink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	return target
}
