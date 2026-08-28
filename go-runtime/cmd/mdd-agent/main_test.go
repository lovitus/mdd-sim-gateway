package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

type processWorker struct{}

func (processWorker) Run(ctx context.Context, ready func()) error {
	ready()
	<-ctx.Done()
	return ctx.Err()
}

func TestAgentProcessHelper(t *testing.T) {
	path := os.Getenv("MDD_AGENT_TEST_CONFIG")
	if path == "" {
		return
	}
	settings, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := runHost(ctx, settings, processWorker{}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentHostProcessAndCLIShareOneControllerAndSingleton(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process interrupt test requires Unix signal behavior")
	}
	root := t.TempDir()
	settings := testConfig(t)
	path := filepath.Join(root, "agent.json")
	payload, _ := json.Marshal(settings)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestAgentProcessHelper$")
	command.Env = append(os.Environ(), "MDD_AGENT_TEST_CONFIG="+path)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	waitForState(t, settings, agentcontrol.StateRunning)
	duplicateContext, cancelDuplicate := context.WithCancel(context.Background())
	if err := runHost(duplicateContext, settings, processWorker{}); err == nil {
		cancelDuplicate()
		t.Fatal("duplicate Agent host acquired the singleton control listener")
	}
	cancelDuplicate()

	var output bytes.Buffer
	if err := runClient("stop", settings, &output); err != nil {
		t.Fatal(err)
	}
	waitForState(t, settings, agentcontrol.StateStopped)
	output.Reset()
	if err := runClient("start", settings, &output); err != nil {
		t.Fatal(err)
	}
	waitForState(t, settings, agentcontrol.StateRunning)

	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("Agent process exit: %v\n%s", err, stderr.String())
	}
	stopped = true
}

func TestAgentConfigRejectsUnknownFieldsLoosePermissionsAndEnabledModem(t *testing.T) {
	root := t.TempDir()
	settings := testConfig(t)
	payload, _ := json.Marshal(settings)
	payload = append(payload[:len(payload)-1], []byte(`,"unknown":true}`)...)
	path := filepath.Join(root, "agent.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("unknown Agent configuration field was accepted")
	}
	settings.Agent.ModemEnabled = true
	payload, _ = json.Marshal(settings)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "PC/SC-only") {
		t.Fatalf("enabled modem error=%v", err)
	}
	if runtime.GOOS != "windows" {
		settings.Agent.ModemEnabled = false
		payload, _ = json.Marshal(settings)
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "0600") {
			t.Fatalf("loose permission error=%v", err)
		}
	}
}

func testConfig(t *testing.T) config {
	t.Helper()
	settings := config{Version: 1, ScanIntervalMS: 100, RetryBaseMS: 100, RetryCapMS: 1000, OperationTimeoutSeconds: 2}
	settings.Agent.ID = "agent-1"
	settings.Agent.ServerURL = "wss://127.0.0.1:8443/v1/agent/ws"
	settings.Agent.ServerToken = "0123456789abcdef0123456789abcdef"
	settings.Agent.TLSFingerprint = strings.Repeat("00", 32)
	settings.Control.Listen = availableAgentAddress(t)
	settings.Control.Token = "abcdef0123456789abcdef0123456789"
	return settings
}

func availableAgentAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func waitForState(t *testing.T, settings config, expected agentcontrol.State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var output bytes.Buffer
		err := runClient("status", settings, &output)
		if err == nil {
			var snapshot agentcontrol.Snapshot
			if json.Unmarshal(output.Bytes(), &snapshot) == nil && snapshot.State == expected {
				return
			}
		} else {
			var apiError *agentcontrol.APIError
			if errors.As(err, &apiError) {
				t.Fatalf("status API error=%v", err)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Agent did not reach state %s", expected)
}
