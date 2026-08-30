package egressexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
)

type fakeProcess struct {
	running bool
	signals int
	stops   int
}

func (process *fakeProcess) Running() bool { return process != nil && process.running }
func (process *fakeProcess) Signal(os.Signal) error {
	if !process.running {
		return errors.New("not running")
	}
	process.signals++
	return nil
}
func (process *fakeProcess) Stop(time.Duration) error {
	process.stops++
	process.running = false
	return nil
}

type fakeController struct {
	starts     int
	checks     int
	waits      int
	checkError error
	startError error
	waitErrors []error
	processes  []*fakeProcess
}

func (controller *fakeController) Check(context.Context, string, string) error {
	controller.checks++
	return controller.checkError
}
func (controller *fakeController) Start(string, string) (managedProcess, error) {
	controller.starts++
	if controller.startError != nil {
		return nil, controller.startError
	}
	process := &fakeProcess{running: true}
	controller.processes = append(controller.processes, process)
	return process, nil
}
func (controller *fakeController) WaitReady(context.Context, []int, managedProcess, time.Duration) error {
	controller.waits++
	if len(controller.waitErrors) == 0 {
		return nil
	}
	err := controller.waitErrors[0]
	controller.waitErrors = controller.waitErrors[1:]
	return err
}

func writeEgressDocument(t *testing.T, path string, document egressdesired.Document) {
	t.Helper()
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestExecutor(t *testing.T, document egressdesired.Document) (*executor, *fakeController, string) {
	t.Helper()
	directory := t.TempDir()
	desiredPath := filepath.Join(directory, "desired.json")
	statusPath := filepath.Join(directory, "status.json")
	writeEgressDocument(t, desiredPath, document)
	controller := &fakeController{}
	now := time.Unix(100, 0)
	runner := &executor{settings: Settings{
		DesiredPath: desiredPath, StatusPath: statusPath, StateDir: directory,
		SingBoxPath: "/usr/local/bin/sing-box", PortBase: proxyPortBase, Poll: time.Second,
	}, controller: controller, now: func() time.Time { return now }}
	return runner, controller, statusPath
}

func readExecutorStatus(t *testing.T, path string) Status {
	t.Helper()
	var status Status
	payload, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(payload, &status) != nil {
		t.Fatalf("read status err=%v payload=%s", err, payload)
	}
	return status
}

func TestExecutorStartsOnceAndDoesNotReloadUnchangedGeneration(t *testing.T) {
	document := egressDocument(strings.Repeat("1", 64), "ss://aes-128-gcm:secret@192.0.2.1:8388")
	runner, controller, statusPath := newTestExecutor(t, document)
	runner.reconcile(context.Background())
	if controller.starts != 1 || controller.checks != 1 || controller.waits != 1 || runner.applied != document.Generation {
		t.Fatalf("starts=%d checks=%d waits=%d applied=%s", controller.starts, controller.checks, controller.waits, runner.applied)
	}
	status := readExecutorStatus(t, statusPath)
	if !status.Ready || status.DesiredGeneration != document.Generation || !status.Exits["gb"].Ready {
		t.Fatalf("status=%+v", status)
	}
	runner.reconcile(context.Background())
	if controller.starts != 1 || controller.checks != 1 || controller.processes[0].signals != 0 {
		t.Fatalf("unchanged generation was reapplied: controller=%+v process=%+v", controller, controller.processes[0])
	}
}

func TestExecutorRestoresPreviousGenerationAfterFailedReload(t *testing.T) {
	first := egressDocument(strings.Repeat("2", 64), "ss://aes-128-gcm:one@192.0.2.2:8388")
	runner, controller, statusPath := newTestExecutor(t, first)
	runner.reconcile(context.Background())
	oldConfig := append([]byte(nil), runner.appliedConfig...)
	second := egressDocument(strings.Repeat("3", 64), "ss://aes-128-gcm:two@192.0.2.3:8388")
	writeEgressDocument(t, runner.settings.DesiredPath, second)
	controller.waitErrors = []error{errors.New("new listeners failed"), nil}
	runner.reconcile(context.Background())
	status := readExecutorStatus(t, statusPath)
	current, err := os.ReadFile(filepath.Join(runner.settings.StateDir, "sing-box.json"))
	if err != nil || !strings.Contains(status.Error, "previous generation restored") || !status.Ready ||
		status.DesiredGeneration != first.Generation || status.RequestedGeneration != second.Generation ||
		runner.applied != first.Generation || controller.starts != 2 || !controller.processes[1].running ||
		string(current) != string(oldConfig) {
		t.Fatalf("status=%+v applied=%s starts=%d currentErr=%v", status, runner.applied, controller.starts, err)
	}
	runner.nextAttempt = time.Time{}
	runner.reconcile(context.Background())
	if controller.starts != 2 || controller.processes[1].signals != 0 {
		t.Fatalf("failed desired generation disrupted restored runtime again: starts=%d signals=%d",
			controller.starts, controller.processes[1].signals)
	}
}

func TestExecutorRecoversCrashedChildWithoutRestartingItsService(t *testing.T) {
	document := egressDocument(strings.Repeat("4", 64), "ss://aes-128-gcm:secret@192.0.2.4:8388")
	runner, controller, statusPath := newTestExecutor(t, document)
	runner.reconcile(context.Background())
	controller.processes[0].running = false
	runner.reconcile(context.Background())
	status := readExecutorStatus(t, statusPath)
	if controller.starts != 2 || !controller.processes[1].running || !status.Ready || status.DesiredGeneration != document.Generation {
		t.Fatalf("starts=%d status=%+v", controller.starts, status)
	}
}

func TestExecutorKeepsLastKnownGoodRuntimeWhenDesiredFileIsTemporarilyUnavailable(t *testing.T) {
	document := egressDocument(strings.Repeat("5", 64), "ss://aes-128-gcm:secret@192.0.2.5:8388")
	runner, controller, statusPath := newTestExecutor(t, document)
	runner.reconcile(context.Background())
	if err := os.Remove(runner.settings.DesiredPath); err != nil {
		t.Fatal(err)
	}
	runner.reconcile(context.Background())
	status := readExecutorStatus(t, statusPath)
	if controller.starts != 1 || !controller.processes[0].running || !status.Ready ||
		status.DesiredGeneration != document.Generation || status.Error != "desired state unavailable" || !status.Exits["gb"].Ready {
		t.Fatalf("status=%+v controller=%+v", status, controller)
	}
}

func TestExecutorRejectsBadCandidateWithoutDisturbingLastKnownGoodRuntime(t *testing.T) {
	first := egressDocument(strings.Repeat("6", 64), "ss://aes-128-gcm:one@192.0.2.6:8388")
	runner, controller, statusPath := newTestExecutor(t, first)
	runner.reconcile(context.Background())
	second := egressDocument(strings.Repeat("7", 64), "ss://aes-128-gcm:two@192.0.2.7:8388")
	writeEgressDocument(t, runner.settings.DesiredPath, second)
	controller.checkError = errors.New("invalid candidate")
	runner.reconcile(context.Background())
	status := readExecutorStatus(t, statusPath)
	if controller.starts != 1 || controller.processes[0].signals != 0 || !controller.processes[0].running ||
		!status.Ready || status.DesiredGeneration != first.Generation || status.RequestedGeneration != second.Generation ||
		!strings.Contains(status.Error, "configuration rejected") {
		t.Fatalf("status=%+v controller=%+v", status, controller)
	}
}
