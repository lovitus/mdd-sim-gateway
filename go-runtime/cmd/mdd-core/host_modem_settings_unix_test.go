//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostModemSettingsRejectValidationAgentAndRedactSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	payload := `{"agent":{"id":"host-agent","server_url":"wss://10.44.0.23:9443/v1/agent/ws","server_token":"private-fixture","pins":{"card":"secret-fixture"}}}`
	if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readHostModemConfig(path, "host-agent", "wss://10.44.0.23:8443/v1/agent/ws"); err == nil {
		t.Fatal("validation Agent accepted as production")
	}
	payload = strings.ReplaceAll(payload, ":9443", ":8443")
	if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, snapshot, err := readHostModemConfig(path, "host-agent", "wss://10.44.0.23:8443/v1/agent/ws")
	if err != nil || snapshot.Settings.Backend != "auto" || len(snapshot.Revision) != 64 {
		t.Fatal("owned configuration not read", err)
	}
	public, _ := json.Marshal(snapshot)
	if strings.Contains(string(public), "fixture") || strings.Contains(string(public), "pins") || strings.Contains(string(public), "token") {
		t.Fatal("public snapshot leaked credentials")
	}
	if _, _, _, err := readHostModemConfig(path, "another-agent", "wss://10.44.0.23:8443/v1/agent/ws"); err == nil {
		t.Fatal("different host instance accepted")
	}
}

func TestHostAgentReadyUsesLocalControlStateNotOnlyProcessPresence(t *testing.T) {
	state := "starting"
	identity, _ := hostModemRuntimeConfig([]byte(`{}`))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			t.Error("unexpected control request", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"state": state, "generation": 1, "runtime_config": identity})
	}))
	defer server.Close()
	payload, _ := json.Marshal(map[string]any{"control": map[string]string{"listen": strings.TrimPrefix(server.URL, "http://"), "token": strings.Repeat("x", 32)}})
	if err := verifyHostAgentReady(context.Background(), payload); err == nil {
		t.Fatal("starting process counted as ready")
	}
	state = "running"
	if err := verifyHostAgentReady(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if _, err := hostModemControl([]byte(`{"control":{"listen":"example.com:1234","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}`)); err == nil {
		t.Fatal("nonlocal control endpoint accepted")
	}
}

func TestHostModemHelperRequiresProductionBinding(t *testing.T) {
	service := &providerApplyService{}
	if _, err := service.HostModemSettings(context.Background()); err == nil {
		t.Fatal("unconfigured helper adopted conventional Agent path")
	}
	service.settings.Public.Listen = "0.0.0.0:8443"
	service.settings.HostModem = &provideradmin.HostModemBinding{ConfigPath: "/var/lib/mdd-agent/config.json", AgentID: "linux-importer-23", CoreURL: "wss://10.44.0.23:9443/v1/agent/ws"}
	if _, err := service.hostModemBinding(); err == nil {
		t.Fatal("production helper accepted validation Core binding")
	}
}

func TestHostAgentProcessMustUseBoundConfiguration(t *testing.T) {
	const path = "/var/lib/mdd-agent/config.json"
	for _, args := range []string{"/usr/libexec/mdd/mdd-agent\x00run\x00-config\x00" + path, "/usr/libexec/mdd/mdd-agent\x00run\x00--config=" + path} {
		if err := validateHostAgentArguments(bytes.Split([]byte(args), []byte{0}), path); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range []string{"/usr/bin/other\x00-config\x00" + path, "/usr/libexec/mdd/mdd-agent\x00run", "/usr/libexec/mdd/mdd-agent\x00-config\x00/validation/config.json", "/usr/libexec/mdd/mdd-agent\x00-config\x00" + path + "\x00-config\x00" + path} {
		if err := validateHostAgentArguments(bytes.Split([]byte(args), []byte{0}), path); err == nil {
			t.Fatal("unbound process accepted")
		}
	}
}

func TestHostModemSavePreservesCredentialsSwitchesAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	const endpoint = "wss://10.44.0.23:8443/v1/agent/ws"
	before := []byte(`{"agent":{"id":"host-agent","server_url":"wss://10.44.0.23:8443/v1/agent/ws","server_token":"fixture-token","pins":{"card":"fixture-pin"},"modem_enabled":false,"modem_sim_apdu_enabled":false,"custom":{"keep":true}},"control":{"keep":"value"}}`)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, current, err := readHostModemConfig(path, "host-agent", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveHostModemConfig(path, "host-agent", endpoint, current.Revision, current.Settings); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(before, unchanged) {
		t.Fatal("no-op save changed file")
	}
	next := hostModemSettings{Backend: "serial", Profiles: []hostModemProfile{{VID: "2c7c", PID: "0125", Name: "Original EC25"}}}
	saved, err := saveHostModemConfig(path, "host-agent", endpoint, current.Revision, next)
	if err != nil || saved.Revision == current.Revision || saved.Settings.Backend != "serial" {
		t.Fatal("mode not saved", err)
	}
	backup, err := os.ReadFile(path + ".before-modem-change")
	if err != nil || !bytes.Equal(before, backup) {
		t.Fatal("original backup not preserved", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var previous, updated map[string]any
	json.Unmarshal(before, &previous)
	json.Unmarshal(after, &updated)
	delete(updated["agent"].(map[string]any), "modem_backend")
	delete(updated["agent"].(map[string]any), "modem_profiles")
	a, _ := json.Marshal(previous)
	b, _ := json.Marshal(updated)
	if !bytes.Equal(a, b) {
		t.Fatal("mode save modified unrelated state")
	}
	if _, err := saveHostModemConfig(path, "host-agent", endpoint, current.Revision, current.Settings); err == nil {
		t.Fatal("stale save overwrote mode")
	}
	if _, err := saveHostModemConfig(path, "host-agent", "wss://10.44.0.23:9443/v1/agent/ws", saved.Revision, current.Settings); err == nil {
		t.Fatal("wrong Core changed mode")
	}
}
