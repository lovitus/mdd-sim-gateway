package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
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
	settings.Local.Token = strings.Repeat("c", 32)
	settings.AuthPath = filepath.Join(directory, "auth.json")
	settings.EventsPath = filepath.Join(directory, "events.db")
	settings.MessagesPath = filepath.Join(directory, "messages.db")
	settings.CatalogPath = catalogPath
	configPath := filepath.Join(directory, "core.json")
	catalog, err := linecatalog.Open(catalogPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	line := linecatalog.Line{
		ID: "line-1", Enabled: true, CardID: "8944100000000000001",
		SIM:     linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"},
		Network: linecatalog.NetworkConfig{EgressCountry: "gb"},
	}
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	snapshotHandler, err := linecatalog.NewSnapshotHandler(catalog, settings.Local.Token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(snapshotHandler)
	defer server.Close()
	defer catalog.Close()
	settings.Local.Listen = strings.TrimPrefix(server.URL, "http://")
	configPayload, _ := json.Marshal(settings)
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	outputDirectory := filepath.Join(directory, "rendered")
	egressStatusPath := filepath.Join(directory, "proxy-status.json")
	if err := os.WriteFile(egressStatusPath, []byte(`{"exits":{"gb":{"ready":true,"host_proxy_host":"127.0.0.1","proxy_port":22157}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runProviderRender([]string{
		"-config", configPath, "-output", outputDirectory, "-state-dir", filepath.Join(directory, "state"),
		"-egress-status", egressStatusPath,
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
	if json.Unmarshal(manifestPayload, &manifest) != nil || manifest.CatalogRevision != 2 || len(manifest.Providers) != 1 {
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
			EPDGAddress: "epdg.example", PCSCF: []string{"pcscf.example"}, EgressCountry: "gb",
		},
		IMS: linecatalog.IMSConfig{
			AccessNetworkInfo: `IEEE-802.11;i-wlan-node-id="020000000001";country=GB`,
			VisitedNetworkID:  "visited.example",
			AccessType:        "wlan1",
			UserEqualsPhone:   true,
			Network:           "udp",
			Expires:           600,
		},
	}
	disabled := line
	disabled.ID, disabled.CardID, disabled.Enabled = "line-disabled", "8944100000000000002", false
	snapshot := linecatalog.Snapshot{SchemaVersion: 1, Revision: 7, Lines: []linecatalog.Line{line, disabled}}
	stateDirectory := filepath.Join(directory, "state")
	firstDirectory, secondDirectory := filepath.Join(directory, "first"), filepath.Join(directory, "second")
	first, err := renderProviderDirectory(settings, snapshot, testEgressStatus(), firstDirectory, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderProviderDirectory(settings, snapshot, testEgressStatus(), secondDirectory, stateDirectory)
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
	if _, err := renderProviderDirectory(settings, snapshot, testEgressStatus(), tamperedDirectory, stateDirectory); err != nil {
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
		provider.Network.ProxyURL != "socks5://127.0.0.1:22157" || provider.Network.MTU != proxiedProviderMTU ||
		provider.IMS.UserAgent != "MDD-Sim-Gateway" || provider.IMS.AccessNetworkInfo != line.IMS.AccessNetworkInfo ||
		provider.IMS.VisitedNetworkID != line.IMS.VisitedNetworkID || provider.IMS.AccessType != "wlan1" ||
		!provider.IMS.UserEqualsPhone ||
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
	_, err := renderProviderDirectory(settings, linecatalog.Snapshot{}, testEgressStatus(), output, filepath.Join(directory, "state"))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error=%v", err)
	}
	if _, statErr := os.Stat(output); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal(statErr)
	}
}

func TestRenderProviderDirectoryFailsClosedWithoutHostLoopbackExit(t *testing.T) {
	directory := t.TempDir()
	settings := config{}
	settings.Local.Listen = "127.0.0.1:39002"
	settings.Local.Token = strings.Repeat("c", 32)
	line := linecatalog.Line{
		SchemaVersion: linecatalog.SchemaVersion, ID: "line-1", Enabled: true,
		CardID:  "8944100000000000001",
		SIM:     linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"},
		Network: linecatalog.NetworkConfig{EgressCountry: "gb"},
	}
	oldStatus := egressstatus.Snapshot{Exits: map[string]egressstatus.Exit{
		"gb": {Ready: true, ProxyPort: 22157},
	}}
	output := filepath.Join(directory, "rendered")
	_, err := renderProviderDirectory(settings, linecatalog.Snapshot{
		SchemaVersion: 1, Revision: 1, Lines: []linecatalog.Line{line},
	}, oldStatus, output, filepath.Join(directory, "state"))
	if err == nil || !strings.Contains(err.Error(), "host loopback proxy") {
		t.Fatalf("unsafe egress error=%v", err)
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed render left output directory: %v", statErr)
	}
}

func testEgressStatus() egressstatus.Snapshot {
	return egressstatus.Snapshot{Exits: map[string]egressstatus.Exit{
		"gb": {Ready: true, HostProxyHost: "127.0.0.1", ProxyPort: 22157},
	}}
}
