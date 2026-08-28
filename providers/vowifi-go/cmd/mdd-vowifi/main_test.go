// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
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

func TestConfigRequiresCompleteLoopbackCoreRegistration(t *testing.T) {
	settings := config{LineID: "line-1", ProviderID: "native", DeviceID: "device-1"}
	settings.IPC.Listen = "127.0.0.1:39001"
	settings.IPC.Token = strings.Repeat("a", 32)
	settings.IPC.StatePath = filepath.Join(t.TempDir(), "operations.db")
	settings.Agent.BrokerToken = strings.Repeat("b", 32)
	settings.Core.RegistrationURL = "http://127.0.0.1:39002/v1/media/providers"
	if err := settings.validate(); err == nil {
		t.Fatal("registration without token was accepted")
	}
	settings.Core.RegistrationToken = strings.Repeat("c", 32)
	settings.Core.RefreshMS = 1000
	if err := settings.validate(); err != nil {
		t.Fatal(err)
	}
	settings.Core.RegistrationURL = "http://192.0.2.1:39002/v1/media/providers"
	if err := settings.validate(); err == nil {
		t.Fatal("remote registration URL was accepted")
	}
}

func TestInitialProviderFactsFailureRemovesRegisteredRoute(t *testing.T) {
	directory := mediaauth.NewProviderDirectory()
	registration, err := mediaauth.NewRegistrationHandler(directory, processTestRegistrationToken)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/media/providers", registration)
	mux.HandleFunc("/v1/provider/facts", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	settings := processTestConfig("127.0.0.1:39001", filepath.Join(t.TempDir(), "operations.db"), "http://127.0.0.1:39002/v1/agent/aka")
	settings.Core.RegistrationURL = server.URL + "/v1/media/providers"
	settings.Core.RegistrationToken = processTestRegistrationToken
	loop, err := providerRegistration(settings, "generation-1", settings.IPC.Listen)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := readyTestSnapshot("generation-1")
	if err := loop.initial(context.Background(), staticSnapshotSource{snapshot: snapshot}); err == nil {
		t.Fatal("initial facts rejection was accepted")
	}
	if _, found := directory.CurrentGeneration(settings.LineID); found {
		t.Fatal("failed initial facts handshake left a routable provider")
	}
}

type staticSnapshotSource struct{ snapshot vowifiipc.Snapshot }

func (source staticSnapshotSource) Status(context.Context) (vowifiipc.Snapshot, error) {
	return source.snapshot, nil
}

func readyTestSnapshot(generation string) vowifiipc.Snapshot {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	return vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-process", ProviderID: "native",
		ProcessGeneration: generation, Sequence: 1, ObservedAt: time.Now().UTC(),
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning, Code: "ready"},
		Tunnel:  ready, IMS: ready, Voice: ready, Messaging: ready,
	}
}
