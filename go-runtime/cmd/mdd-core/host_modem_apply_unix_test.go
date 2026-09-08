//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
)

func TestHostModeApplyDoesNotRepeatHeldOperationOrTouchStaleConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	payload := []byte(`{"agent":{"id":"host","server_url":"wss://10.44.0.23:8443/v1/agent/ws"}}`)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	binding := &provideradmin.HostModemBinding{ConfigPath: path, AgentID: "host", CoreURL: "wss://10.44.0.23:8443/v1/agent/ws"}
	_, info, current, err := readHostModemConfig(path, binding.AgentID, binding.CoreURL)
	if err != nil {
		t.Fatal(err)
	}
	service := &providerApplyService{}
	service.settings.HostModem = binding
	service.settings.Public.Listen = "0.0.0.0:8443"
	input := provideradmin.HostModemRequest{ExpectedRevision: "stale", Settings: current.Settings}
	if _, err := service.applyHostModemSettings(context.Background(), binding, input); err == nil {
		t.Fatal("stale configuration accepted")
	}
	input.ExpectedRevision = current.Revision
	if _, err := service.applyHostModemSettings(context.Background(), binding, input); err != nil {
		t.Fatal("no-op incorrectly required service mutation", err)
	}
	record := hostModeReceipt{SchemaVersion: 1, State: "switching", Held: true, AgentID: "host", LeaseID: "lease-fixture", Revision: current.Revision}
	if err := writeHostModeReceipt(path+".host-mode-status.json", info, record); err != nil {
		t.Fatal(err)
	}
	if _, err := service.applyHostModemSettings(context.Background(), binding, input); err == nil {
		t.Fatal("held operation repeated")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, payload) {
		t.Fatal("admission changed Agent config", err)
	}
	recovery, err := readHostModeReceipt(path + ".host-mode-status.json")
	if err != nil || !recovery.Held || recovery.LeaseID != "lease-fixture" || service.hostSwitching.Load() {
		t.Fatal("interrupted operation evidence was lost", err)
	}
}
