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

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

type processWorker struct{}

func (processWorker) Run(ctx context.Context, ready func()) error {
	ready()
	<-ctx.Done()
	return ctx.Err()
}

func (processWorker) Topology() agentlink.TopologySnapshot {
	return agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{}}
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
	var topologyOutput bytes.Buffer
	if err := runClient("topology", settings, &topologyOutput); err != nil {
		t.Fatal(err)
	}
	var topology agentlink.TopologySnapshot
	if err := json.Unmarshal(topologyOutput.Bytes(), &topology); err != nil || topology.ReaderCondition != agentlink.ReaderReady {
		t.Fatalf("topology=%+v error=%v", topology, err)
	}
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

func TestAgentHostReadyMeansItOwnsTheSingletonListener(t *testing.T) {
	settings := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runHostWithReady(ctx, settings, processWorker{}, func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("host exited before ready: %v", err)
	case <-time.After(time.Second):
		t.Fatal("host did not report listener ownership")
	}

	duplicateReady := false
	err := runHostWithReady(context.Background(), settings, processWorker{}, func() { duplicateReady = true })
	if err == nil {
		t.Fatal("duplicate host acquired singleton listener")
	}
	if duplicateReady {
		t.Fatal("duplicate host reported ready without listener ownership")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAgentConfigRejectsUnknownFieldsLoosePermissionsAndUnsupportedModem(t *testing.T) {
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
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "only on Windows, macOS, and Linux") {
			t.Fatalf("enabled modem error=%v", err)
		}
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
	settings.Agent.ModemEnabled = false
	settings.Agent.ModemSIMAPDU = true
	payload, _ = json.Marshal(settings)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "requires modem_enabled") {
		t.Fatalf("orphan modem SIM APDU error=%v", err)
	}
}

func TestAgentConfigAcceptsLegacyAdvertiseHostCompatibilityField(t *testing.T) {
	root := t.TempDir()
	settings := testConfig(t)
	payload, _ := json.Marshal(settings)
	payload = bytes.Replace(payload, []byte(`"agent":{`), []byte(`"agent":{"advertise_host":"legacy-shadow-host",`), 1)
	path := filepath.Join(root, "agent.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(path)
	if err != nil {
		t.Fatalf("legacy advertise_host was rejected: %v", err)
	}
	if loaded.Agent.AdvertiseHost != "legacy-shadow-host" {
		t.Fatalf("advertise_host=%q", loaded.Agent.AdvertiseHost)
	}
}

func TestLinuxManagedModemFailsClosedUntilPersistentGuardExists(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only fail-closed contract")
	}
	if modem, err := newModemProber(modemProberOptions{Enabled: true, ManagedRuntime: true}); err == nil || modem != nil ||
		(!strings.Contains(err.Error(), "requires root") && !strings.Contains(err.Error(), "persistent MDD cellular guard") &&
			!strings.Contains(err.Error(), "persistent cellular data guard")) {
		t.Fatalf("modem=%v err=%v", modem, err)
	}
}

func TestConfigCommandsCreateOnePrivateSharedConfigWithoutExposingTokens(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private", "agent.json")
	var output bytes.Buffer
	if err := runConfigCommand([]string{"init", "-config", path}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config info=%v err=%v", info, err)
	}
	if strings.Contains(output.String(), "server_token") && !strings.Contains(output.String(), `"server_token":""`) {
		t.Fatalf("initial output exposed a token: %s", output.String())
	}

	updates := []struct {
		arguments []string
		input     string
	}{
		{[]string{"set", "agent_id", "mac-reader-1", "--config=" + path}, ""},
		{[]string{"set", "server", "gateway.example:8443", "--config=" + path}, ""},
		{[]string{"set", "tls_sha256", strings.Repeat("AB:", 31) + "AB", "--config=" + path}, ""},
		{[]string{"set", "token", "--stdin", "--config=" + path}, "0123456789abcdef0123456789abcdef\n"},
		{[]string{"set", "sim_pin", "89010000000000000001", "--stdin", "--config=" + path}, "1234\n"},
	}
	for _, update := range updates {
		output.Reset()
		if err := runConfigCommand(update.arguments, strings.NewReader(update.input), &output); err != nil {
			t.Fatalf("config %v: %v", update.arguments, err)
		}
		if strings.Contains(output.String(), "0123456789abcdef") {
			t.Fatalf("config output exposed the server token: %s", output.String())
		}
		if strings.Contains(output.String(), "1234") {
			t.Fatalf("config output exposed a SIM PIN: %s", output.String())
		}
	}
	settings, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Agent.ServerURL != "wss://gateway.example:8443/v1/agent/ws" || settings.Agent.ID != "mac-reader-1" ||
		settings.Agent.ServerToken != "0123456789abcdef0123456789abcdef" || settings.Agent.TLSFingerprint != strings.Repeat("ab", 32) {
		t.Fatalf("saved settings=%+v", settings.Agent)
	}
	if settings.Agent.PINs["89010000000000000001"] != "1234" || settings.Agent.PINRevisions["89010000000000000001"] == "" {
		t.Fatalf("SIM PIN configuration was not saved")
	}
	output.Reset()
	if err := runConfigCommand([]string{"show", "-config", path}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), settings.Agent.ServerToken) || strings.Contains(output.String(), settings.Control.Token) ||
		!strings.Contains(output.String(), `"ready":true`) || !strings.Contains(output.String(), `"server_token":"\u003credacted\u003e"`) {
		t.Fatalf("unsafe or incomplete config view: %s", output.String())
	}
}

func TestConfigCommandRejectsArgvTokenInvalidIdentityAndUnsafeExistingDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.json")
	if err := runConfigCommand([]string{"set", "token", "secret", "-config", path}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("argv token was accepted")
	}
	if err := runConfigCommand([]string{"set", "agent_id", "not allowed", "-config", path}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("invalid Agent identity was accepted")
	}
	if runtime.GOOS != "windows" {
		unsafeDirectory := filepath.Join(root, "unsafe")
		if err := os.Mkdir(unsafeDirectory, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unsafeDirectory, 0o777); err != nil {
			t.Fatal(err)
		}
		err := runConfigCommand([]string{"init", "-config", filepath.Join(unsafeDirectory, "agent.json")}, strings.NewReader(""), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "writable") {
			t.Fatalf("unsafe directory error=%v", err)
		}
	}
}

func TestManagedServiceHostStopsCooperativelyWithoutUnexpectedExit(t *testing.T) {
	unexpected := make(chan error, 1)
	host, err := newManagedHost(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, func(err error) { unexpected <- err })
	if err != nil {
		t.Fatal(err)
	}
	if err := host.start(); err != nil {
		t.Fatal(err)
	}
	if err := host.start(); err == nil {
		t.Fatal("duplicate service host start succeeded")
	}
	if err := host.stop(time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-unexpected:
		t.Fatalf("cooperative stop reported unexpected exit: %v", err)
	default:
	}
}

func TestManagedServiceHostReportsUnexpectedExit(t *testing.T) {
	exited := make(chan error, 1)
	expected := errors.New("host failed")
	host, err := newManagedHost(func(context.Context) error { return expected }, func(err error) { exited <- err })
	if err != nil {
		t.Fatal(err)
	}
	if err := host.start(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if !errors.Is(err, expected) {
			t.Fatalf("unexpected exit error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unexpected service host exit was not reported")
	}
	if err := host.stop(time.Second); !errors.Is(err, expected) {
		t.Fatalf("stop error=%v", err)
	}
}

func TestManagedServiceHostTreatsUnexpectedNilExitAsFailure(t *testing.T) {
	exited := make(chan error, 1)
	host, err := newManagedHost(func(context.Context) error { return nil }, func(err error) { exited <- err })
	if err != nil {
		t.Fatal(err)
	}
	if err := host.start(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err == nil || !strings.Contains(err.Error(), "exited unexpectedly") {
			t.Fatalf("unexpected nil exit error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unexpected nil service host exit was not reported")
	}
	if err := host.stop(time.Second); err == nil || !strings.Contains(err.Error(), "exited unexpectedly") {
		t.Fatalf("stop error=%v", err)
	}
}

func TestManagedServiceHostWaitsForAcceptedReadiness(t *testing.T) {
	run := make(chan struct{})
	host, err := newManagedHost(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-run:
			<-ctx.Done()
			return ctx.Err()
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	waited := make(chan error, 1)
	go func() { waited <- host.waitReady(ready, time.Second) }()
	select {
	case err := <-waited:
		t.Fatalf("readiness returned before listener ownership: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(run)
	close(ready)
	if err := <-waited; err != nil || !host.readyAccepted() {
		t.Fatalf("readiness error=%v accepted=%t", err, host.readyAccepted())
	}
	if err := host.stop(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestManagedServiceHostExitWinsReadyRace(t *testing.T) {
	expected := errors.New("startup failed")
	ready := make(chan struct{})
	host, err := newManagedHost(func(context.Context) error {
		close(ready)
		return expected
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.start(); err != nil {
		t.Fatal(err)
	}
	if err := host.waitReady(ready, time.Second); !errors.Is(err, expected) {
		t.Fatalf("ready race error=%v, want %v", err, expected)
	}
	if host.readyAccepted() {
		t.Fatal("failed host was accepted as ready")
	}
}

func TestServiceStopTimeoutIsBounded(t *testing.T) {
	settings := config{OperationTimeoutSeconds: 2}
	if got := serviceStopTimeout(settings); got != 7*time.Second {
		t.Fatalf("short stop timeout=%v", got)
	}
	settings.OperationTimeoutSeconds = 60
	if got := serviceStopTimeout(settings); got != 15*time.Second {
		t.Fatalf("bounded stop timeout=%v", got)
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
