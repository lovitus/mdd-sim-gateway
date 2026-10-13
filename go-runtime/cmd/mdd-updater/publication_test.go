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

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemupdate"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
)

type publicationRuntime struct{ effects int }

func (*publicationRuntime) Snapshot() (uint64, []mediaauth.ProviderFence, error) { return 1, nil, nil }
func (*publicationRuntime) Busy() error                                          { return nil }
func (runtime *publicationRuntime) Provider(context.Context, mediaauth.ProviderFence, string, string) error {
	runtime.effects++
	return nil
}
func (runtime *publicationRuntime) FreezeAgents(string) error  { runtime.effects++; return nil }
func (runtime *publicationRuntime) ReleaseAgents(string) error { runtime.effects++; return nil }

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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(401)
					return
				}
				owner.ServeHTTP(w, r)
			}))
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
