//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemupdate"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/releaseinstall"
)

func TestUpdaterReconcileRejectsActiveWorkerBeforeReadingOrChangingState(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("separate hosted root worker invocation check")
	}
	path := filepath.Join(t.TempDir(), "update")
	store, err := systemupdate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	request := systemupdate.Request{OperationID: "update-one", Repository: "example/project", Target: "2.0.0", RequestedAt: time.Now()}
	if err := store.Request(request); err != nil {
		t.Fatal(err)
	}
	active, err := releaseinstall.AcquireExecutionLock(path+".worker.lock", os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if active != nil {
			_ = active.Close()
		}
	}()
	before, err := os.ReadFile(path + ".status.json")
	if err != nil {
		t.Fatal(err)
	}
	err = run([]string{"-state", path, "-destination", t.TempDir(), "-core-config", filepath.Join(t.TempDir(), "absent-config.json"), "-reconcile"})
	if err == nil || !strings.Contains(err.Error(), "another release installation is running") {
		t.Fatalf("ACTIVE_WORKER_CANCEL: reconciliation passed the active execution owner: %v", err)
	}
	after, err := os.ReadFile(path + ".status.json")
	if err != nil || string(before) != string(after) {
		t.Fatal("active worker state was modified", err)
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	active = nil
	// The refused cancellation leaves the original request eligible for its
	// paused worker's normal Begin once download/preflight have completed.
	gate, err := systemupdate.OpenAdmission(store)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := systemupdate.NewMaintenanceOwner(store, gate, &publicationRuntime{}, "core-before", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	target := systemupdate.UpdateTarget{RequestedTarget: request.Target, ReleaseID: "mdd-2.0.0", SourceRevision: strings.Repeat("c", 40), CoreSHA256: strings.Repeat("b", 64), Contract: 1}
	view, err := owner.Begin(t.Context(), request.OperationID, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Cleanup(t.Context(), request.OperationID, view.Record.Revision); err != nil {
		t.Fatal(err)
	}
}
