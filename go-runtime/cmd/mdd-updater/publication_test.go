package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemupdate"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
)

type publicationRuntime struct {
	effects, begins           int
	withProvider, failRelease bool
}

func (runtime *publicationRuntime) Snapshot() (uint64, []mediaauth.ProviderFence, error) {
	if runtime.withProvider {
		return 1, []mediaauth.ProviderFence{{LineID: "line-one", ProviderID: "provider-one", Generation: "generation-one", CardID: "8944100000000000001"}}, nil
	}
	return 1, nil, nil
}
func (*publicationRuntime) Busy() error { return nil }
func (runtime *publicationRuntime) Provider(_ context.Context, _ mediaauth.ProviderFence, _ string, action string) error {
	runtime.effects++
	if action == "begin" {
		runtime.begins++
	}
	if action == "release" && runtime.failRelease {
		return errors.New("provider release unavailable")
	}
	return nil
}
func (runtime *publicationRuntime) FreezeAgents(string) error  { runtime.effects++; return nil }
func (runtime *publicationRuntime) ReleaseAgents(string) error { runtime.effects++; return nil }

func TestUpdaterReconcilesOriginalMaintenanceWithCorruptWorkerStatus(t *testing.T) {
	for _, phase := range []string{systemupdate.MaintenanceCleanup, systemupdate.MaintenanceReleasing, systemupdate.MaintenanceReleased} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "update")
			store, err := systemupdate.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			request := systemupdate.Request{OperationID: "update-one", Repository: "example/project", Target: "2.0.0", RequestedAt: time.Now()}
			if err := store.Request(request); err != nil {
				t.Fatal(err)
			}
			gate, err := systemupdate.OpenAdmission(store)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &publicationRuntime{withProvider: true}
			owner, err := systemupdate.NewMaintenanceOwner(store, gate, runtime, "core-before", strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			target := systemupdate.UpdateTarget{RequestedTarget: request.Target, ReleaseID: "mdd-2.0.0", SourceRevision: strings.Repeat("c", 40), CoreSHA256: strings.Repeat("b", 64), Contract: 1}
			view, err := owner.Begin(t.Context(), request.OperationID, target)
			if err != nil {
				t.Fatal(err)
			}
			runtime.failRelease = phase != systemupdate.MaintenanceReleased
			if phase == systemupdate.MaintenanceCleanup {
				view, _ = owner.Cleanup(t.Context(), request.OperationID, view.Record.Revision)
			} else {
				view, err = owner.Activate(request.OperationID, view.Record.Revision)
				if err != nil {
					t.Fatal(err)
				}
				gate, err = systemupdate.OpenAdmission(store)
				if err != nil {
					t.Fatal(err)
				}
				owner, err = systemupdate.NewMaintenanceOwner(store, gate, runtime, "core-after", target.CoreSHA256)
				if err != nil {
					t.Fatal(err)
				}
				for _, unit := range systemupdate.FixedServices {
					view, err = owner.Restarted(request.OperationID, owner.View().Record.Revision, unit)
					if err != nil {
						t.Fatal(err)
					}
				}
				view, _ = owner.Finish(t.Context(), request.OperationID, view.Record.Revision)
			}
			if view.Record.Phase != phase {
				t.Fatalf("fixture phase=%s", view.Record.Phase)
			}
			if err := os.WriteFile(path+".status.json", []byte("interrupted worker status"), 0600); err != nil {
				t.Fatal(err)
			}
			token := strings.Repeat("t", 32)
			authenticated, err := provideradmin.Authenticate(owner, token)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(authenticated)
			defer server.Close()
			core := coreMaintenance{baseURL: server.URL, token: token}
			view, err = core.maintenance(t.Context(), &systemupdate.MaintenanceCommand{Action: "request", OperationID: request.OperationID, RequestedTarget: request.Target})
			if err != nil || view.Publication == nil || view.Publication.Committed || view.Publication.Unconfirmed == "" {
				t.Fatalf("CORRUPT_STATUS_CLEANUP: %+v %v", view, err)
			}
			runtime.failRelease = false
			worker, err := systemupdate.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := reconcileUpdate(t.Context(), worker, &core, view.Publication.Status, request.OperationID, view); err != nil {
				t.Fatal(err)
			}
			status, err := store.Status()
			want := systemupdate.StateSucceeded
			if phase == systemupdate.MaintenanceCleanup {
				want = systemupdate.StateFailed
			}
			if err != nil || status.State != want || status.OperationID != request.OperationID || status.Target != request.Target || owner.View().GuardInstalled || runtime.begins != 1 {
				t.Fatalf("CORRUPT_STATUS_CLEANUP: %+v %v begins=%d", status, err, runtime.begins)
			}
		})
	}
}

func TestUpdaterReconcilesOnlyOriginalUnactivatedRequest(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "worker-read-lost", true: "half-published"}[missing], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "update")
			store, err := systemupdate.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			request := systemupdate.Request{OperationID: "update-one", Repository: "example/project", Target: "2.0.0", RequestedAt: time.Now()}
			if err := store.Request(request); err != nil {
				t.Fatal(err)
			}
			if missing {
				if err := os.Remove(path + ".status.json"); err != nil {
					t.Fatal(err)
				}
			}
			gate, err := systemupdate.OpenAdmission(store)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &publicationRuntime{}
			owner, err := systemupdate.NewMaintenanceOwner(store, gate, runtime, "core-original", strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			token := strings.Repeat("t", 32)
			authenticated, err := provideradmin.Authenticate(owner, token)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(authenticated)
			defer server.Close()
			core := coreMaintenance{baseURL: server.URL, token: token}
			view, err := core.maintenance(t.Context(), &systemupdate.MaintenanceCommand{Action: "request", OperationID: request.OperationID, RequestedTarget: request.Target})
			if err != nil || view.Publication == nil || view.Publication.Committed == missing {
				t.Fatalf("publication=%+v err=%v", view, err)
			}
			worker, err := systemupdate.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			// The explicit cancellation reports failure, but must persist the exact
			// original terminal receipt without acquiring maintenance or installing.
			_ = reconcileUpdate(t.Context(), worker, &core, view.Publication.Status, request.OperationID, view)
			status, err := store.Status()
			if err != nil || status.State != systemupdate.StateFailed || status.OperationID != request.OperationID || status.Target != request.Target || runtime.effects != 0 {
				t.Fatalf("UNSTARTED_RECONCILE: status=%+v effects=%d err=%v", status, runtime.effects, err)
			}
			if err := store.Request(systemupdate.Request{OperationID: "update-two", Repository: request.Repository, Target: "3.0.0", RequestedAt: time.Now()}); err != nil {
				t.Fatal("original cleanup did not restore request admission", err)
			}
			if _, err := core.maintenance(t.Context(), &systemupdate.MaintenanceCommand{Action: "request", OperationID: request.OperationID, RequestedTarget: request.Target}); err == nil {
				t.Fatal("stale worker publication was accepted")
			}
		})
	}
}

func TestUpdaterUsesProductionMaintenanceAuthentication(t *testing.T) {
	token := strings.Repeat("t", 32)
	for _, credentials := range []struct {
		name, token string
		accepted    bool
	}{{"valid", token, true}, {"wrong", strings.Repeat("w", 32), false}, {"empty", "", false}} {
		t.Run(credentials.name, func(t *testing.T) {
			directory := t.TempDir()
			store, err := systemupdate.Open(filepath.Join(directory, "update"))
			if err != nil {
				t.Fatal(err)
			}
			request := systemupdate.Request{OperationID: "update-one", Repository: "example/project", Target: "2.0.0", RequestedAt: time.Now()}
			if err := store.Request(request); err != nil {
				t.Fatal(err)
			}
			gate, err := systemupdate.OpenAdmission(store)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &publicationRuntime{}
			owner, err := systemupdate.NewMaintenanceOwner(store, gate, runtime, "original-core", strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			authenticated, err := provideradmin.Authenticate(owner, token)
			if err != nil {
				t.Fatal(err)
			}
			codes := make(chan int, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorded := httptest.NewRecorder()
				authenticated.ServeHTTP(recorded, r)
				response := recorded.Result()
				defer response.Body.Close()
				for name, values := range response.Header {
					w.Header()[name] = values
				}
				w.WriteHeader(response.StatusCode)
				_, _ = io.Copy(w, response.Body)
				codes <- response.StatusCode
			}))
			defer server.Close()
			core := coreMaintenance{baseURL: server.URL, token: credentials.token}
			readState := func() map[string]string {
				t.Helper()
				entries, err := os.ReadDir(directory)
				if err != nil {
					t.Fatal(err)
				}
				files := make(map[string]string)
				for _, entry := range entries {
					payload, err := os.ReadFile(filepath.Join(directory, entry.Name()))
					if err != nil {
						t.Fatal(err)
					}
					files[entry.Name()] = string(payload)
				}
				return files
			}
			before, viewBefore := readState(), owner.View()
			for _, method := range []string{"get", "post"} {
				t.Run(method, func(t *testing.T) {
					var command *systemupdate.MaintenanceCommand
					if method == "post" {
						command = &systemupdate.MaintenanceCommand{Action: "request", OperationID: request.OperationID, RequestedTarget: request.Target}
					}
					view, err := core.maintenance(t.Context(), command)
					code := <-codes
					if credentials.accepted {
						if err != nil || code != http.StatusOK || view.Instance != viewBefore.Instance || view.CoreSHA256 != viewBefore.CoreSHA256 {
							t.Fatalf("PRODUCTION_MAINTENANCE_AUTH: %s status=%d view=%+v error=%v", method, code, view, err)
						}
						if method == "post" && (view.Publication == nil || !view.Publication.Committed || view.Publication.Request.OperationID != request.OperationID || view.Publication.Status.Target != request.Target) {
							t.Fatalf("original request not returned: %+v", view.Publication)
						}
					} else if err == nil || code != http.StatusUnauthorized {
						t.Fatalf("bad credential not rejected: status=%d error=%v", code, err)
					}
				})
			}
			if runtime.effects != 0 || !reflect.DeepEqual(owner.View(), viewBefore) || !reflect.DeepEqual(readState(), before) {
				t.Fatal("maintenance authentication/readback changed durable state or admission")
			}
		})
	}
}
