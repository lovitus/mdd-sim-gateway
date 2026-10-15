package egressexec

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
)

func xrayRendered(t *testing.T, generation string) Rendered {
	t.Helper()
	document := egressDocument(generation, "")
	document.Proxy.Profiles["node"] = egressconfig.Profile{Type: "subscription"}
	result, err := renderAtBase(document, proxyPortBase, map[string][]subscriptionNode{"node": {xhttpNode(t)}}, nil)
	if err != nil || len(result.XrayConfig) == 0 || len(result.XrayPorts) != 1 {
		t.Fatal("XHTTP did not render both configurations", err)
	}
	return result
}

func TestXrayPairActivationRollbackAndChildHealth(t *testing.T) {
	one, two := strings.Repeat("1", 64), strings.Repeat("2", 64)
	runner, sing, path := newTestExecutor(t, egressDocument(one, ""))
	xray := &fakeController{}
	runner.xrayController = xray
	runner.settings.XrayPath = "/usr/local/bin/xray"
	first := xrayRendered(t, one)
	runner.activateXrayPair(context.Background(), one, first)
	if !runner.runtimeReady() || sing.starts != 1 || xray.starts != 1 || !readExecutorStatus(t, path).Ready {
		t.Fatal("paired activation failed")
	}
	for _, name := range []string{"sing-box.json", "xray.json"} {
		info, err := os.Stat(filepath.Join(runner.settings.StateDir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("configuration not private", err)
		}
	}
	xray.checkError = errors.New("fixture secret must not escape")
	runner.activateXrayPair(context.Background(), two, xrayRendered(t, two))
	status := readExecutorStatus(t, path)
	if sing.processes[0].stops != 0 || xray.processes[0].stops != 0 || !status.Ready || strings.Contains(status.Error, "secret") {
		t.Fatal("rejected candidate interrupted prior pair or exposed raw output")
	}
	xray.checkError = nil
	sing.waitErrors = []error{errors.New("candidate listener failed"), nil}
	runner.activateXrayPair(context.Background(), two, xrayRendered(t, two))
	status = readExecutorStatus(t, path)
	if !status.Ready || status.DesiredGeneration != one || runner.applied != one || !runner.runtimeReady() ||
		sing.starts != 3 || xray.starts != 3 {
		t.Fatal("failed activation did not restore both prior processes", status)
	}
	if xray.processes[1].stops != 1 || sing.processes[1].stops != 1 {
		t.Fatal("failed candidate process leaked")
	}
	xray.processes[2].running = false
	if runner.runtimeReady() {
		t.Fatal("dead Xray child reported ready")
	}
}

func TestXrayPairCanReturnToOrdinarySingBox(t *testing.T) {
	one, two := strings.Repeat("1", 64), strings.Repeat("2", 64)
	runner, sing, _ := newTestExecutor(t, egressDocument(one, ""))
	xray := &fakeController{}
	runner.xrayController = xray
	runner.activateXrayPair(context.Background(), one, xrayRendered(t, one))
	ordinary, err := Render(egressDocument(two, "ss://aes-128-gcm:fixture@192.0.2.1:8388"))
	if err != nil {
		t.Fatal(err)
	}
	runner.activateXrayPair(context.Background(), two, ordinary)
	if !runner.runtimeReady() || runner.xrayChild != nil || xray.processes[0].stops != 1 || xray.starts != 1 || sing.starts != 2 {
		t.Fatal("Xray retained after ordinary configuration activated")
	}
	if _, err := os.Stat(filepath.Join(runner.settings.StateDir, "xray.json")); !os.IsNotExist(err) {
		t.Fatal("old Xray credentials retained", err)
	}
}

func TestXrayStartupFailureNeverStartsSingBoxOrPublishesReady(t *testing.T) {
	one := strings.Repeat("1", 64)
	runner, sing, path := newTestExecutor(t, egressDocument(one, ""))
	xray := &fakeController{waitErrors: []error{errors.New("not listening")}}
	runner.xrayController = xray
	runner.activateXrayPair(context.Background(), one, xrayRendered(t, one))
	if sing.starts != 0 || xray.processes[0].stops != 1 || readExecutorStatus(t, path).Ready || runner.runtimeReady() {
		t.Fatal("Xray failure admitted country exit")
	}
}

func TestXHTTPOnlySubscriptionReconcilesAndDoesNotRestartUnchangedPair(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xhttpFixture))
	}))
	defer server.Close()
	generation := strings.Repeat("a", 64)
	document := egressDocument(generation, "")
	document.Proxy.Profiles["node"] = egressconfig.Profile{Type: "subscription", URL: server.URL, RefreshMinutes: 1}
	runner, sing, path := newTestExecutor(t, document)
	xray := &fakeController{}
	runner.xrayController = xray
	runner.reconcile(context.Background())
	if !readExecutorStatus(t, path).Ready || sing.starts != 1 || xray.starts != 1 {
		t.Fatal("XHTTP-only subscription rejected before activation")
	}
	runner.reconcile(context.Background())
	if sing.starts != 1 || xray.starts != 1 || sing.processes[0].signals != 0 {
		t.Fatal("unchanged pair restarted")
	}
	xray.processes[0].running = false
	runner.reconcile(context.Background())
	if !runner.runtimeReady() || sing.starts != 1 || xray.starts != 2 || sing.processes[0].stops != 0 || sing.processes[0].signals != 0 {
		t.Fatal("dead bridge recovery interrupted its healthy consumer")
	}
	document.Proxy.Enabled = false
	document.Generation = strings.Repeat("b", 64)
	writeEgressDocument(t, runner.settings.DesiredPath, document)
	runner.reconcile(context.Background())
	if runner.child != nil || runner.xrayChild != nil || sing.processes[0].stops != 1 || xray.processes[1].stops != 1 {
		t.Fatal("disabled proxy retained a process")
	}
}

func TestFailedBridgeRecoveryKeepsHealthySingBoxButDoesNotReportReady(t *testing.T) {
	generation := strings.Repeat("c", 64)
	runner, sing, path := newTestExecutor(t, egressDocument(generation, ""))
	xray := &fakeController{}
	runner.xrayController = xray
	result := xrayRendered(t, generation)
	runner.activateXrayPair(context.Background(), generation, result)
	xray.processes[0].running = false
	xray.waitErrors = []error{errors.New("bridge failed to listen")}
	runner.restoreXrayBridge(context.Background(), generation, result)
	if !sing.processes[0].running || sing.processes[0].stops != 0 || sing.processes[0].signals != 0 || sing.starts != 1 {
		t.Fatal("failed bridge recovery interrupted other exits")
	}
	if readExecutorStatus(t, path).Ready || runner.runtimeReady() || xray.processes[1].running {
		t.Fatal("failed bridge was advertised ready or leaked")
	}
	if !runner.nextAttempt.After(runner.now()) {
		t.Fatal("failed recovery omitted backoff")
	}
}

func TestUnchangedXrayPairRestoresMissingSingBoxOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xhttpFixture))
	}))
	defer server.Close()
	document := egressDocument(strings.Repeat("e", 64), "")
	document.AttemptID = "unchanged-apply"
	document.Proxy.Profiles["node"] = egressconfig.Profile{Type: "subscription", URL: server.URL, RefreshMinutes: 1}
	runner, sing, path := newTestExecutor(t, document)
	xray := &fakeController{}
	runner.xrayController = xray
	runner.reconcile(t.Context())
	before := readExecutorStatus(t, path)
	if !runner.runtimeReady() {
		t.Fatal("fixture pair did not activate")
	}
	sing.processes[0].running = false
	runner.reconcile(t.Context())
	after := readExecutorStatus(t, path)
	if !runner.runtimeReady() || !after.RuntimeConfirmed || after.PendingApply || after.AttemptID != before.AttemptID ||
		after.ConfigIdentity != before.ConfigIdentity || sing.starts != 2 || xray.starts != 1 || xray.processes[0].stops != 0 {
		t.Fatal("unchanged consumer did not recover without replacing its bridge", after)
	}
	sing.processes[1].running = false
	sing.waitErrors = []error{errors.New("consumer listeners unavailable")}
	sing.stopError = errors.New("consumer stop unconfirmed")
	runner.reconcile(t.Context())
	unconfirmed := runner.child
	runner.reconcile(t.Context())
	after = readExecutorStatus(t, path)
	if after.RuntimeConfirmed || after.Ready || runner.runtimeReady() || runner.child != unconfirmed || !unconfirmed.Running() ||
		sing.starts != 3 || xray.starts != 1 || xray.processes[0].stops != 0 {
		t.Fatal("unconfirmed consumer was certified or discarded on the next tick", after)
	}
	sing.stopError, sing.processes[2].stopError = nil, nil
	deadline := runner.nextAttempt
	runner.now = func() time.Time { return deadline.Add(time.Second) }
	runner.reconcile(t.Context())
	if !runner.runtimeReady() || !readExecutorStatus(t, path).RuntimeConfirmed || sing.starts != 4 || xray.starts != 1 ||
		sing.processes[2].running || xray.processes[0].stops != 0 {
		t.Fatal("confirmed consumer cleanup did not allow scoped recovery")
	}
}

func TestDeadXrayAndChangedFeedRequireNewMaintenanceAttempt(t *testing.T) {
	var mu sync.Mutex
	feed := xhttpFixture
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write([]byte(feed))
	}))
	defer server.Close()
	document := egressDocument(strings.Repeat("d", 64), "")
	document.AttemptID = "initial-apply"
	document.Proxy.Profiles["node"] = egressconfig.Profile{Type: "subscription", URL: server.URL, RefreshMinutes: 1}
	document.Proxy.Exits["hk"] = document.Proxy.Exits["gb"]
	document.Proxy.Exits["gb"] = egressconfig.Exit{Enabled: true, Mode: "direct"}
	runner, sing, path := newTestExecutor(t, document)
	xray := &fakeController{}
	runner.xrayController = xray
	now := time.Now()
	runner.now = func() time.Time { return now }
	runner.reconcile(t.Context())
	if !runner.runtimeReady() {
		t.Fatal("fixture pair did not activate")
	}
	xray.processes[0].running = false
	mu.Lock()
	feed = strings.ReplaceAll(feed, "/transport", "/replacement")
	mu.Unlock()
	now = now.Add(2 * time.Minute)
	runner.reconcile(t.Context())
	status := readExecutorStatus(t, path)
	if !status.PendingApply || status.RuntimeConfirmed || !status.Exits["gb"].Ready || status.Exits["hk"].Ready ||
		sing.starts != 1 || sing.processes[0].stops != 0 {
		t.Fatalf("dead bridge authorized interruption of healthy country: %+v starts=%d stops=%d", status, sing.starts, sing.processes[0].stops)
	}
	document.AttemptID = "explicit-drained-apply"
	writeEgressDocument(t, runner.settings.DesiredPath, document)
	runner.reconcile(t.Context())
	if !runner.runtimeReady() || sing.starts != 2 || !readExecutorStatus(t, path).RuntimeConfirmed {
		t.Fatal("new maintenance attempt did not apply")
	}
}
