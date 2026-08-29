package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func TestLegacyImportCommandWritesOnlyNewCatalog(t *testing.T) {
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "new", "lines.db")
	settings := config{}
	settings.Public.Listen = "127.0.0.1:8443"
	settings.Public.TLSCert = filepath.Join(directory, "tls.crt")
	settings.Public.TLSKey = filepath.Join(directory, "tls.key")
	settings.Local.Listen = "127.0.0.1:39002"
	settings.Local.Token = localToken
	settings.AuthPath = filepath.Join(directory, "auth.json")
	settings.EventsPath = filepath.Join(directory, "events.db")
	settings.MessagesPath = filepath.Join(directory, "messages.db")
	settings.CatalogPath = catalogPath
	configPayload, _ := json.Marshal(settings)
	configPath := filepath.Join(directory, "core.json")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(directory, "config.yaml")
	source := []byte(`instances:
  old-1:
    name: Imported
    iccid: "8944100000000000001"
    imsi: "234100000000001"
    mcc: "234"
    mnc: "10"
    ami_secret: ignored
`)
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	egressDesiredPath := filepath.Join(directory, "desired.json")
	if err := os.WriteFile(egressDesiredPath, []byte(`{"version":1,"lines":[{"id":"old-1","enabled":true,"country":"gb"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runLegacyImport([]string{"-config", configPath, "-source", sourcePath, "-egress-desired", egressDesiredPath}, &output); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if json.Unmarshal(output.Bytes(), &result) != nil || result["status"] != "imported" || result["lines"] != float64(1) {
		t.Fatalf("output=%s", output.String())
	}
	if err := runLegacyImport([]string{"-config", configPath, "-source", sourcePath, "-egress-desired", egressDesiredPath}, &bytes.Buffer{}); !errors.Is(err, linecatalog.ErrNotEmpty) {
		t.Fatalf("second import error=%v", err)
	}
	store, err := linecatalog.Open(catalogPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot()
	if err != nil || snapshot.Revision != 2 || len(snapshot.Lines) != 1 || snapshot.Lines[0].ID != "old-1" ||
		snapshot.Lines[0].Network.EgressCountry != "gb" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestEgressImportCommandKeepsSecretsOutOfOutput(t *testing.T) {
	directory := t.TempDir()
	settings := config{}
	settings.Public.Listen = "127.0.0.1:8443"
	settings.Public.TLSCert = filepath.Join(directory, "tls.crt")
	settings.Public.TLSKey = filepath.Join(directory, "tls.key")
	settings.Local.Listen = "127.0.0.1:39002"
	settings.Local.Token = localToken
	settings.AuthPath = filepath.Join(directory, "auth.json")
	settings.EventsPath = filepath.Join(directory, "events.db")
	settings.MessagesPath = filepath.Join(directory, "messages.db")
	settings.CatalogPath = filepath.Join(directory, "lines.db")
	settings.EgressPath = filepath.Join(directory, "egress.db")
	configPayload, _ := json.Marshal(settings)
	configPath := filepath.Join(directory, "core.json")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "ss://must-not-appear-in-command-output"
	sourcePath := filepath.Join(directory, "desired.json")
	source := map[string]any{"version": 1, "proxy": map[string]any{
		"schema_version": 2, "enabled": true, "missing_policy": "error", "refresh_minutes": 30,
		"profiles": map[string]any{"node": map[string]any{"name": "London", "type": "node", "value": secret}},
		"exits":    map[string]any{"gb": map[string]any{"enabled": true, "profile_id": "node", "pin_mode": "auto"}},
	}, "hardware": map[string]any{"auto_detect": true}}
	payload, _ := json.Marshal(source)
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runEgressImport([]string{"-config", configPath, "-source", sourcePath}, &output); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte(secret)) || !bytes.Contains(output.Bytes(), []byte(`"profiles":1`)) {
		t.Fatalf("unsafe or incomplete import output=%s", output.String())
	}
	store, err := egressconfig.Open(settings.EgressPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot()
	if err != nil || snapshot.Config.Profiles["node"].Value != secret || snapshot.Config.Exits["gb"].PinMode != "" {
		t.Fatalf("imported snapshot=%+v err=%v", snapshot, err)
	}
}
