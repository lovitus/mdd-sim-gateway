package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
)

func TestRenderProviderCommandReadsCatalogAndWritesNewDirectory(t *testing.T) {
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "lines.db")
	settings := config{}
	settings.Public.Listen = "127.0.0.1:8443"
	settings.Public.TLSCert = filepath.Join(directory, "tls.crt")
	settings.Public.TLSKey = filepath.Join(directory, "tls.key")
	settings.Local.Listen = "127.0.0.1:39002"
	settings.Local.Token = strings.Repeat("c", 32)
	settings.AuthPath = filepath.Join(directory, "auth.json")
	settings.EventsPath = filepath.Join(directory, "events.db")
	settings.MessagesPath = filepath.Join(directory, "messages.db")
	settings.CatalogPath = catalogPath
	configPayload, _ := json.Marshal(settings)
	configPath := filepath.Join(directory, "core.json")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := linecatalog.Open(catalogPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	line := linecatalog.Line{
		ID: "line-1", Enabled: true, CardID: "8944100000000000001",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"},
	}
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	outputDirectory := filepath.Join(directory, "rendered")
	var output bytes.Buffer
	if err := runProviderRender([]string{
		"-config", configPath, "-output", outputDirectory, "-state-dir", filepath.Join(directory, "state"),
	}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"status":"rendered"`) {
		t.Fatalf("output=%s", output.String())
	}
	manifestPayload, err := os.ReadFile(filepath.Join(outputDirectory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest providerconfig.Manifest
	if json.Unmarshal(manifestPayload, &manifest) != nil || manifest.CatalogRevision != 1 || len(manifest.Providers) != 1 {
		t.Fatalf("manifest=%s", manifestPayload)
	}
}

func TestRenderProviderDirectoryIsDeterministicAndUsesDynamicIPC(t *testing.T) {
	directory := t.TempDir()
	settings := config{}
	settings.Local.Listen = "localhost:39002"
	settings.Local.Token = strings.Repeat("c", 32)
	line := linecatalog.Line{
		SchemaVersion: linecatalog.SchemaVersion, ID: "line-1", Name: "UK", Enabled: true,
		CardID: "8944100000000000001",
		SIM:    linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: "123456789012345"},
		Network: linecatalog.NetworkConfig{
			EPDGAddress: "epdg.example", PCSCF: []string{"pcscf.example"},
		},
		IMS: linecatalog.IMSConfig{Network: "udp", Expires: 600},
	}
	disabled := line
	disabled.ID, disabled.CardID, disabled.Enabled = "line-disabled", "8944100000000000002", false
	snapshot := linecatalog.Snapshot{SchemaVersion: 1, Revision: 7, Lines: []linecatalog.Line{line, disabled}}
	stateDirectory := filepath.Join(directory, "state")
	firstDirectory, secondDirectory := filepath.Join(directory, "first"), filepath.Join(directory, "second")
	first, err := renderProviderDirectory(settings, snapshot, firstDirectory, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderProviderDirectory(settings, snapshot, secondDirectory, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if first.CatalogRevision != 7 || len(first.Providers) != 1 || first.Providers[0] != second.Providers[0] {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	firstPayload, err := os.ReadFile(filepath.Join(firstDirectory, first.Providers[0].ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	secondPayload, _ := os.ReadFile(filepath.Join(secondDirectory, second.Providers[0].ConfigFile))
	if string(firstPayload) != string(secondPayload) {
		t.Fatal("same catalog and Core settings produced different provider configs")
	}
	loaded, err := providerconfig.LoadDirectory(firstDirectory)
	if err != nil || loaded.CatalogRevision != 7 || len(loaded.Providers) != 1 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	tamperedDirectory := filepath.Join(directory, "tampered")
	if _, err := renderProviderDirectory(settings, snapshot, tamperedDirectory, stateDirectory); err != nil {
		t.Fatal(err)
	}
	tamperedConfig := filepath.Join(tamperedDirectory, first.Providers[0].ConfigFile)
	if err := os.WriteFile(tamperedConfig, append(firstPayload, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := providerconfig.LoadDirectory(tamperedDirectory); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered config error=%v", err)
	}
	var provider providerconfig.Config
	if err := json.Unmarshal(firstPayload, &provider); err != nil {
		t.Fatal(err)
	}
	if err := provider.Validate(); err != nil {
		t.Fatal(err)
	}
	if provider.IPC.Listen != "127.0.0.1:0" || provider.Core.RegistrationURL != "http://127.0.0.1:39002/v1/media/providers" ||
		provider.Agent.BrokerURL != "http://127.0.0.1:39002/v1/agent/aka" || provider.Agent.CardID != line.CardID ||
		provider.IPC.Token == settings.Local.Token || len(provider.IPC.Token) != 64 {
		t.Fatalf("provider=%+v", provider)
	}
	manifestPayload, _ := os.ReadFile(filepath.Join(firstDirectory, "manifest.json"))
	if strings.Contains(string(manifestPayload), settings.Local.Token) || strings.Contains(string(manifestPayload), disabled.ID) {
		t.Fatalf("manifest leaked secret or disabled provider: %s", manifestPayload)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(firstDirectory, first.Providers[0].ConfigFile))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("provider config mode=%04o", info.Mode().Perm())
		}
	}
}

func TestRenderProviderDirectoryRefusesExistingOutput(t *testing.T) {
	directory := t.TempDir()
	settings := config{}
	settings.Local.Listen = "127.0.0.1:39002"
	settings.Local.Token = strings.Repeat("c", 32)
	output := filepath.Join(directory, "existing")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := renderProviderDirectory(settings, linecatalog.Snapshot{}, output, filepath.Join(directory, "state"))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error=%v", err)
	}
	if _, statErr := os.Stat(output); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal(statErr)
	}
}
