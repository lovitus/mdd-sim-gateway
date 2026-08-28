// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRequiresStrictJSONAndPrivateFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "vowifi.json")
	payload := `{
  "line_id":"line-1","provider_id":"native","device_id":"device-1","trace_id":"trace-1",
  "ipc":{"listen":"127.0.0.1:39001","token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state_path":"STATE_PATH"},
  "agent":{"broker_url":"http://127.0.0.1:39002/v1/agent/aka","broker_token":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","id":"agent-1","process_generation":"agent-process-1","session_generation":"card-session-1","card_id":"8944100000000000001"},
  "sim":{"imsi":"234100000000001","mcc":"234","mnc":"10"},
  "network":{"epdg_address":"epdg.example"},"ims":{}
}`
	payload = strings.Replace(payload, "STATE_PATH", filepath.Join(directory, "operations.db"), 1)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadConfig(path)
	if err != nil || settings.LineID != "line-1" || settings.upstream().Agent.CardID != "8944100000000000001" {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSuffix(payload, "}")+`,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("unknown config field was accepted")
	}
}

func TestConfigRejectsNonLoopbackIPC(t *testing.T) {
	settings := config{LineID: "line-1", ProviderID: "native", DeviceID: "device-1"}
	settings.IPC.Listen = "0.0.0.0:39001"
	settings.IPC.Token = strings.Repeat("a", 32)
	settings.IPC.StatePath = filepath.Join(t.TempDir(), "operations.db")
	settings.Agent.BrokerToken = strings.Repeat("b", 32)
	if err := settings.validate(); err == nil {
		t.Fatal("non-loopback IPC was accepted")
	}
}
