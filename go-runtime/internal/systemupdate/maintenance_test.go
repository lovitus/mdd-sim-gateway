package systemupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
)

// Only external maintenance I/O is simulated. The update owner, durable file,
// admission, restart transitions and reopen use the production implementation.
type updateRuntime struct {
	fences                 []mediaauth.ProviderFence
	leases                 map[string]string
	busy                   bool
	freeze                 string
	loseBegin, loseRelease string
	ended                  []string
}

func (runtime *updateRuntime) Snapshot() (uint64, []mediaauth.ProviderFence, error) {
	return 3, append([]mediaauth.ProviderFence(nil), runtime.fences...), nil
}
func (runtime *updateRuntime) Busy() error {
	if runtime.busy {
		return errors.New("active media")
	}
	return nil
}
func (runtime *updateRuntime) FreezeAgents(lease string) error { runtime.freeze = lease; return nil }
func (runtime *updateRuntime) ReleaseAgents(lease string) error {
	if runtime.freeze != "" && runtime.freeze != lease {
		return errors.New("foreign lease")
	}
	runtime.freeze = ""
	return nil
}
func (runtime *updateRuntime) Provider(_ context.Context, fence mediaauth.ProviderFence, lease, action string) error {
	found := false
	for _, current := range runtime.fences {
		if current == fence {
			found = true
		}
	}
	if !found {
		return errors.New("provider replaced")
	}
	switch action {
	case "begin":
		runtime.leases[fence.LineID] = lease
		if runtime.loseBegin == fence.LineID {
			return errors.New("lost begin response")
		}
	case "verify":
		if runtime.leases[fence.LineID] != lease {
			return errors.New("lease missing")
		}
	case "release":
		if current := runtime.leases[fence.LineID]; current != "" && current != lease {
			return errors.New("foreign lease")
		}
		delete(runtime.leases, fence.LineID)
		runtime.ended = append(runtime.ended, fence.LineID)
		if runtime.loseRelease == fence.LineID {
			return errors.New("lost release response")
		}
	}
	return nil
}

func updateTestOwner(t *testing.T, count int) (*MaintenanceOwner, *updateRuntime, *Store) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "update"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Request(Request{OperationID: "update-one", Repository: "example/project", Target: "2.0.0", RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	runtime := &updateRuntime{leases: map[string]string{}}
	for i := 0; i < count; i++ {
		id := string(rune('a' + i))
		runtime.fences = append(runtime.fences, mediaauth.ProviderFence{LineID: "line-" + id, ProviderID: "provider-" + id, Generation: "generation-" + id, CardID: "8944100000000000001"})
	}
	gate, err := OpenAdmission(store)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewMaintenanceOwner(store, gate, runtime, "core-before", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return owner, runtime, store
}
func updateTestTarget() UpdateTarget {
	return UpdateTarget{RequestedTarget: "2.0.0", ReleaseID: "mdd-2.0.0", SourceRevision: strings.Repeat("c", 40), CoreSHA256: strings.Repeat("b", 64), Contract: 1}
}

func TestUpdateMaintenanceSurvivesCoreReplacementAndReleasesOnlyOriginalSet(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			owner, runtime, store := updateTestOwner(t, count)
			view, err := owner.Begin(t.Context(), "update-one", updateTestTarget())
			if err != nil {
				t.Fatal(err)
			}
			if !view.GuardInstalled || view.Record.Phase != MaintenanceReady {
				t.Fatal("UPDATE_GUARD: maintenance was not installed")
			}
			view, err = owner.Activate("update-one", view.Record.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Activate("update-one", view.Record.Revision); err == nil {
				t.Fatal("REPEAT_ACTIVATE: destructive stage was repeatable")
			}
			if _, err := owner.Finish(t.Context(), "update-one", view.Record.Revision); err == nil {
				t.Fatal("OLD_CORE_HEALTH: old Core released maintenance")
			}
			reopened, err := Open(strings.TrimSuffix(store.requestPath, ".request.json"))
			if err != nil {
				t.Fatal(err)
			}
			gate, err := OpenAdmission(reopened)
			if err != nil {
				t.Fatal(err)
			}
			next, err := NewMaintenanceOwner(reopened, gate, runtime, "core-after", updateTestTarget().CoreSHA256)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := gate.Enter(); err == nil {
				t.Fatal("RESTORED_GUARD: new Core admitted business")
			}
			if _, err := next.Registration(mediaauth.Provider{LineID: "new-line", ProviderID: "new-provider", Generation: "new-generation"}); err == nil {
				t.Fatal("LATE_PROVIDER: absent provider bypassed maintenance")
			}
			if _, err := next.Finish(t.Context(), "update-one", view.Record.Revision); err == nil {
				t.Fatal("MISSING_RESTARTS: released before fixed service confirmation")
			}
			for _, unit := range FixedServices {
				view, err = next.Restarted("update-one", next.View().Record.Revision, unit)
				if err != nil {
					t.Fatal(err)
				}
			}
			view, err = next.Finish(t.Context(), "update-one", view.Record.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if view.GuardInstalled || view.Record.Phase != MaintenanceReleased || len(runtime.ended) != count {
				t.Fatalf("RELEASE_SET: %+v ended=%v", view, runtime.ended)
			}
			if _, err := next.Begin(t.Context(), "update-one", updateTestTarget()); err == nil {
				t.Fatal("COMPLETED_RETRY: lagging worker status allowed another install")
			}
		})
	}
}

func TestUpdateAdmissionRejectsInflightWritesAndBusyMediaWithoutFencingOldCalls(t *testing.T) {
	for _, kind := range []string{"write", "media"} {
		t.Run(kind, func(t *testing.T) {
			owner, runtime, _ := updateTestOwner(t, 0)
			if kind == "write" {
				done, err := owner.gate.Enter()
				if err != nil {
					t.Fatal(err)
				}
				defer done()
			} else {
				runtime.busy = true
			}
			if _, err := owner.Begin(t.Context(), "update-one", updateTestTarget()); err == nil {
				t.Fatal("BUSY_ADMISSION: update accepted active business")
			}
			if runtime.freeze != "" || owner.gate.Blocked() {
				t.Fatal("REJECTED_UPDATE_DISRUPTED_CALL: busy rejection left an RPC fence")
			}
		})
	}
}

func TestUpdateLostDrainAndPartialReleaseKeepOriginalCleanupAcrossReopen(t *testing.T) {
	owner, runtime, store := updateTestOwner(t, 2)
	runtime.loseBegin, runtime.loseRelease = "line-b", "line-a"
	view, err := owner.Begin(t.Context(), "update-one", updateTestTarget())
	if err == nil || view.Record == nil || view.Record.Phase != MaintenanceCleanup || !view.GuardInstalled {
		t.Fatalf("LOST_DRAIN: %+v %v", view, err)
	}
	if runtime.leases["line-b"] == "" {
		t.Fatal("lost successful drain not represented by cleanup owner")
	}
	gate, err := OpenAdmission(store)
	if err != nil {
		t.Fatal(err)
	}
	next, err := NewMaintenanceOwner(store, gate, runtime, "core-after", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	runtime.loseRelease = ""
	view, err = next.Cleanup(t.Context(), "update-one", view.Record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if view.GuardInstalled || len(runtime.leases) != 0 || view.Record.VerifiedCore != "" {
		t.Fatalf("cleanup changed activation outcome: %+v", view)
	}
}

func TestUpdateHTTPAdmissionKeepsReadbackAndCallCleanup(t *testing.T) {
	gate := &Admission{}
	if err := gate.freeze("update-one"); err != nil {
		t.Fatal(err)
	}
	handler := gate.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/v1/system/update/progress", 204}, {"POST", "/api/auth/login", 204},
		{"POST", "/v1/lines/line-a/vowifi/calls/end", 204}, {"POST", "/v1/lines/line-a/vowifi/calls/start", 409},
		{"POST", "/v1/lines/line-a/cellular/calls/hangup", 204}, {"POST", "/v1/lines/line-a/cellular/calls/reject", 204},
		{"DELETE", "/v1/cellular/media/leases", 204}, {"DELETE", "/v1/media/leases", 204},
		{"POST", "/v1/cellular/media/leases", 409}, {"POST", "/v1/lines/line-a/cellular/calls/answer", 409},
		{"PUT", "/v1/catalog/lines/line-a", 409},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		if w.Code != test.status {
			t.Fatalf("%s %s: %d", test.method, test.path, w.Code)
		}
	}
}

func TestUpdateStatusCannotHidePendingMaintenanceOrReleasedReceipt(t *testing.T) {
	owner, _, store := updateTestOwner(t, 0)
	view, err := owner.Begin(t.Context(), "update-one", updateTestTarget())
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{StateIdle, StateFailed, StateSucceeded} {
		if err := store.SetStatus(Status{OperationID: "update-one", State: state, UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		status, err := store.Status()
		if err != nil || status.State != StateUnknown || status.Maintenance == nil {
			t.Fatalf("PENDING_STATUS: maintenance hidden by %s: %+v %v", state, status, err)
		}
	}
	if err := os.WriteFile(store.statusPath, []byte("interrupted publication"), 0600); err != nil {
		t.Fatal(err)
	}
	if status, err := store.Status(); err != nil || status.State != StateUnknown || status.OperationID != "update-one" {
		t.Fatalf("UNREADABLE_STATUS: %+v %v", status, err)
	}
	if err := store.SetStatus(Status{OperationID: "update-one", State: StateRunning, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Cleanup(t.Context(), "update-one", view.Record.Revision); err != nil {
		t.Fatal(err)
	}
	status, err := store.Status()
	if err != nil || status.State != StateUnknown || status.ErrorCode != "update_release_receipt_pending" || status.Maintenance.Phase != MaintenanceReleased {
		t.Fatalf("RELEASED_STATUS: %+v %v", status, err)
	}
	if err := store.Request(Request{OperationID: "another-update", Repository: "example/project", Target: "3.0.0", RequestedAt: time.Now()}); err == nil {
		t.Fatal("UNCONFIRMED_WORKER: new request replaced original receipt")
	}
}

func TestUpdateIdentityAndPersistenceGatePrecedeExternalEffects(t *testing.T) {
	t.Run("unsupported target", func(t *testing.T) {
		owner, runtime, _ := updateTestOwner(t, 1)
		target := updateTestTarget()
		target.Contract = 0
		if _, err := owner.Begin(t.Context(), "update-one", target); err == nil || len(runtime.leases) != 0 || owner.gate.Blocked() {
			t.Fatal("UNSUPPORTED_TARGET: maintenance started")
		}
	})
	t.Run("durable write failed", func(t *testing.T) {
		owner, runtime, store := updateTestOwner(t, 1)
		// The record path becomes a directory after admission was opened.
		if err := os.Mkdir(store.maintenancePath, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Begin(t.Context(), "update-one", updateTestTarget()); err == nil || len(runtime.leases) != 0 || runtime.freeze != "" {
			t.Fatal("PERSIST_BEFORE_DRAIN: external effect without durable owner")
		}
	})
	t.Run("replacement route", func(t *testing.T) {
		owner, runtime, _ := updateTestOwner(t, 1)
		view, err := owner.Begin(t.Context(), "update-one", updateTestTarget())
		if err != nil {
			t.Fatal(err)
		}
		runtime.fences[0].Generation = "replacement"
		view, err = owner.Cleanup(t.Context(), "update-one", view.Record.Revision)
		if err == nil || !view.GuardInstalled || view.Record.Phase != MaintenanceCleanup || len(runtime.ended) != 0 {
			t.Fatal("REPLACED_PROVIDER: cleanup released a different process")
		}
	})
}
