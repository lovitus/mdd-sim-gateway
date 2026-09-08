//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

func TestEgressApplyUsesExactSnapshotsAndWaitsForRuntimeGeneration(t *testing.T) {
	directory := t.TempDir()
	token := strings.Repeat("t", 32)
	egressStore, err := egressconfig.Open(filepath.Join(directory, "egress.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer egressStore.Close()
	egressDesired := egressconfig.Config{SchemaVersion: egressconfig.SchemaVersion, Enabled: true, MissingPolicy: "error",
		RefreshMinutes: 30, Profiles: map[string]egressconfig.Profile{
			"node": {Name: "London", Type: "node", Value: "ss://secret"},
		}, Exits: map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "node"}}}
	if err := egressStore.ImportEmpty(egressDesired, egressconfig.ImportReceipt{SourceSHA256: strings.Repeat("a", 64), ImportedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	catalogStore, err := linecatalog.Open(filepath.Join(directory, "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogStore.Close()
	if _, err := catalogStore.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Name: "GB", Enabled: true,
		CardID: "8944100000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"},
		Network: linecatalog.NetworkConfig{EgressCountry: "gb"}}); err != nil {
		t.Fatal(err)
	}
	egressSnapshotHandler, _ := egressconfig.NewSnapshotHandler(egressStore, token)
	catalogSnapshotHandler, _ := linecatalog.NewSnapshotHandler(catalogStore, token)
	mux := http.NewServeMux()
	mux.Handle(egressconfig.SnapshotIPCPath, egressSnapshotHandler)
	mux.Handle(linecatalog.SnapshotIPCPath, catalogSnapshotHandler)
	var maintenanceBlocked atomic.Bool
	var changeConfigOnDrain atomic.Bool
	var maintenanceMu sync.Mutex
	var maintenanceTrace []bool
	maintenanceRequests := func() []bool {
		maintenanceMu.Lock()
		defer maintenanceMu.Unlock()
		return append([]bool(nil), maintenanceTrace...)
	}
	maintenance := func(begin bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(401)
				return
			}
			var input providerapply.DrainRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				w.WriteHeader(400)
				return
			}
			maintenanceMu.Lock()
			maintenanceTrace = append(maintenanceTrace, begin)
			maintenanceMu.Unlock()
			code := "resumed"
			if begin {
				code = "drained"
				if changeConfigOnDrain.Swap(false) {
					latest, err := egressStore.Snapshot()
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					profile := latest.Config.Profiles["node"]
					profile.Name = "Changed during drain"
					latest.Config.Profiles["node"] = profile
					if _, err := egressStore.PutExpected(latest.Config, latest.Revision); err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
				}
			}
			ready := true
			if begin && maintenanceBlocked.Load() {
				code = "active_call"
				ready = false
				w.WriteHeader(409)
			}
			result := providerapply.DrainResult{SchemaVersion: 1, CatalogRevision: input.CatalogRevision, LeaseID: input.LeaseID, Ready: ready, Code: code}
			for _, id := range input.LineIDs {
				result.Lines = append(result.Lines, providerapply.DrainLineResult{LineID: id, Code: code})
			}
			json.NewEncoder(w).Encode(result)
		}
	}
	mux.Handle(providerapply.DrainPath, maintenance(true))
	mux.Handle(providerapply.ResumePath, maintenance(false))
	server := httptest.NewServer(mux)
	defer server.Close()

	desiredPath := filepath.Join(directory, "desired.json")
	statusPath := filepath.Join(directory, "status.json")
	legacy := `{"version":1,"proxy":{"schema_version":2,"enabled":true,"missing_policy":"error","refresh_minutes":30,"profiles":{},"exits":{}},"hardware":{"auto_detect":true,"modem_profiles":{"keep":{}}},"lines":[],"generation":"` + strings.Repeat("0", 64) + `","updated_at":1}`
	if err := os.WriteFile(desiredPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(desiredPath)
	settings := config{}
	settings.Local.Listen = server.Listener.Addr().String()
	settings.Local.Token = token
	settings.ProviderApply.EgressDesiredPath = desiredPath
	settings.ProviderApply.EgressStatusPath = statusPath
	service := &providerApplyService{settings: settings, uid: os.Getuid(), gid: os.Getgid(), desiredUID: os.Getuid()}

	_, err = service.ApplyEgress(context.Background(), 999, 999)
	var failure *egressconfig.ApplyError
	if !errors.As(err, &failure) || failure.Code != "egress_apply_revision_changed" {
		t.Fatalf("revision mismatch err=%v", err)
	}
	afterMismatch, _ := os.ReadFile(desiredPath)
	if string(afterMismatch) != string(before) {
		t.Fatal("revision mismatch published desired state")
	}

	egressSnapshot, _ := egressStore.Snapshot()
	catalogSnapshot, _ := catalogStore.Snapshot()
	maintenanceBlocked.Store(true)
	_, err = service.ApplyEgress(context.Background(), egressSnapshot.Revision, catalogSnapshot.Revision)
	if !errors.As(err, &failure) || failure.Code != "egress_maintenance_blocked" {
		t.Fatalf("active call gate: %v", err)
	}
	afterBlocked, _ := os.ReadFile(desiredPath)
	if string(afterBlocked) != string(before) {
		t.Fatal("blocked drain published desired state")
	}
	maintenanceBlocked.Store(false)
	expected, err := egressdesired.Render(egressSnapshot, catalogSnapshot, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	statusPayload, _ := json.Marshal(map[string]any{"desired_generation": expected.Generation})
	if err := os.WriteFile(statusPath, statusPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyEgress(context.Background(), egressSnapshot.Revision, catalogSnapshot.Revision)
	if err != nil || result.Code != "runtime_confirmed" || result.Generation != expected.Generation || result.State != "applied" {
		t.Fatalf("apply result=%+v err=%v", result, err)
	}
	if trace := maintenanceRequests(); len(trace) != 3 || !trace[1] || trace[2] {
		t.Fatalf("maintenance order=%v", trace)
	}
	document, err := egressdesired.Read(desiredPath)
	if err != nil || document.Version != 2 || document.EgressConfigRevision != egressSnapshot.Revision ||
		document.CatalogRevision != catalogSnapshot.Revision || len(document.Hardware) != 0 || len(document.Lines) != 0 {
		t.Fatalf("published document=%+v err=%v", document, err)
	}
	status, err := service.EgressStatus(context.Background())
	if err != nil || status.Pending || !status.RuntimeConfirmed || status.DesiredGeneration != expected.Generation {
		t.Fatalf("apply status=%+v err=%v", status, err)
	}

	result, err = service.ApplyEgress(context.Background(), egressSnapshot.Revision, catalogSnapshot.Revision)
	if err != nil || result.State != "unchanged" {
		t.Fatalf("unchanged apply=%+v err=%v", result, err)
	}

	if err := os.Remove(desiredPath); err != nil {
		t.Fatal(err)
	}
	status, err = service.EgressStatus(context.Background())
	if err != nil || !status.Pending || status.RuntimeConfirmed || status.AppliedConfig != 0 || status.AppliedCatalog != 0 {
		t.Fatalf("missing desired status=%+v err=%v", status, err)
	}
	result, err = service.ApplyEgress(context.Background(), egressSnapshot.Revision, catalogSnapshot.Revision)
	if err != nil || result.State != "applied" || result.Generation != expected.Generation {
		t.Fatalf("first clean apply=%+v err=%v", result, err)
	}
	beforeConcurrent, err := os.ReadFile(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	changeConfigOnDrain.Store(true)
	_, err = service.ApplyEgress(context.Background(), egressSnapshot.Revision, catalogSnapshot.Revision)
	if !errors.As(err, &failure) || failure.Code != "egress_apply_revision_changed" {
		t.Fatal("configuration changed during drain was applied", err)
	}
	afterConcurrent, err := os.ReadFile(desiredPath)
	if err != nil || string(beforeConcurrent) != string(afterConcurrent) {
		t.Fatal("stale apply published desired state", err)
	}
	trace := maintenanceRequests()
	if len(trace) < 2 || !trace[len(trace)-2] || trace[len(trace)-1] {
		t.Fatal("rejected apply did not release its lease", trace)
	}
	egressSnapshot, err = egressStore.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statusPath); err != nil {
		t.Fatal(err)
	}
	beforeTimeout := len(maintenanceRequests())
	bounded, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err = service.ApplyEgress(bounded, egressSnapshot.Revision, catalogSnapshot.Revision)
	if !errors.As(err, &failure) || failure.Code != "egress_apply_unconfirmed_maintenance_retained" {
		t.Fatalf("unknown application error=%v", err)
	}
	if trace := maintenanceRequests(); len(trace) != beforeTimeout+1 || !trace[len(trace)-1] {
		t.Fatal("unconfirmed application resumed admission")
	}
}

func TestEgressDesiredBoundaryIsReadOnlyToExecutorAndAllowsFirstApply(t *testing.T) {
	root := t.TempDir()
	settings := config{}
	settings.ProviderApply.CandidateRoot = filepath.Join(root, "candidates")
	settings.ProviderApply.ReceiptPath = filepath.Join(root, "receipts")
	settings.ProviderApply.EgressDesiredPath = filepath.Join(root, "egress", "desired.json")
	for path, mode := range map[string]os.FileMode{
		settings.ProviderApply.CandidateRoot:                   0o755,
		settings.ProviderApply.ReceiptPath:                     0o700,
		filepath.Dir(settings.ProviderApply.EgressDesiredPath): 0o750,
	} {
		if err := os.Mkdir(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateProviderApplyRoots(settings, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("missing first desired file was rejected: %v", err)
	}
	if err := os.WriteFile(settings.ProviderApply.EgressDesiredPath, []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := validateProviderApplyRoots(settings, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("root:service 0640 desired file was rejected: %v", err)
	}
	if err := os.Chmod(settings.ProviderApply.EgressDesiredPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateProviderApplyRoots(settings, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("executor-unreadable desired file was accepted")
	}
}
