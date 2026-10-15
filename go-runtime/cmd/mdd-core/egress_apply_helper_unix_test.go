//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulardata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
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
			"node":   {Name: "London", Type: "node", Value: "ss://aes-128-gcm:fixture@192.0.2.1:8388"},
			"broken": {Name: "Unavailable", Type: "node", Value: "unsupported://fixture"},
		}, Exits: map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "node"}, "hk": {Enabled: true, ProfileID: "broken"}}}
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
	mux.HandleFunc(cellulardata.InternalPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var input struct {
			CardID    string    `json:"card_id"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		port := 32123
		if input.CardID == "8985200000000000002" {
			port++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "owner-" + input.CardID, "line_id": "donor",
			"state": "ready", "listen_port": port, "username": "fixture", "password": "fixture",
			"expires_at": input.ExpiresAt, "max_bytes": uint64(1 << 40), "used_bytes": 0})
	})
	var maintenanceBlocked atomic.Bool
	var changeConfigOnDrain atomic.Bool
	var maintenanceMu sync.Mutex
	var maintenanceTrace []bool
	var maintenanceLease string
	mux.HandleFunc(providerapply.Path, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			return
		}
		snapshot, _ := catalogStore.Snapshot()
		maintenanceMu.Lock()
		lease := maintenanceLease
		maintenanceMu.Unlock()
		json.NewEncoder(w).Encode(providerapply.Snapshot{SchemaVersion: 1, CatalogRevision: snapshot.Revision,
			Lines: []providerapply.LineStatus{{LineID: "line-1", ProviderPresent: true, Code: "provider_reachable",
				ProcessGeneration: "provider-1", Maintenance: vowifiipc.MaintenanceStatus{Draining: lease != "", LeaseID: lease}}}})
	})
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
			maintenanceMu.Lock()
			if ready && begin {
				maintenanceLease = input.LeaseID
			}
			if !begin && maintenanceLease == input.LeaseID {
				maintenanceLease = ""
			}
			maintenanceMu.Unlock()
			result := providerapply.DrainResult{SchemaVersion: 1, CatalogRevision: input.CatalogRevision, LeaseID: input.LeaseID, Ready: ready, Code: code}
			for _, id := range input.LineIDs {
				result.Lines = append(result.Lines, providerapply.DrainLineResult{LineID: id, Code: code, ProcessGeneration: "provider-1"})
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
	stopExecutor := startEgressFixture(t, directory, desiredPath, statusPath, server.URL)
	result, err := service.ApplyEgress(context.Background(), egressSnapshot.Revision, catalogSnapshot.Revision)
	if err != nil || result.Code != "runtime_confirmed" || result.Generation != expected.Generation || result.State != "partial" || len(result.FailedCountries) != 1 || result.FailedCountries[0] != "hk" {
		t.Fatalf("apply result=%+v err=%v", result, err)
	}
	if trace := maintenanceRequests(); len(trace) != 3 || !trace[1] || trace[2] {
		t.Fatalf("maintenance order=%v", trace)
	}
	runtime, err := egressdesired.ReadRuntime(statusPath)
	if err != nil || !runtime.Exits["gb"].Ready || runtime.Exits["hk"].Ready || runtime.Exits["hk"].ProxyPort != 0 {
		t.Fatalf("partial result lost healthy exit or exposed failed exit: %+v %v", runtime, err)
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(runtime.Exits["gb"].ProxyPort)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
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
	if err != nil || result.State != "partial" {
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
	if err != nil || result.State != "partial" || result.Generation != expected.Generation {
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
	stopExecutor()
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
	// A restarted helper must resume the durable original lease, not invent a
	// new lease or reapply the same request while its result is still unknown.
	beforeRecovery := len(maintenanceRequests())
	startEgressFixture(t, directory, desiredPath, statusPath, server.URL)
	restarted := &providerApplyService{settings: settings, uid: os.Getuid(), gid: os.Getgid(), desiredUID: os.Getuid()}
	recovered, err := restarted.recoverEgressAttempt(context.Background(), server.URL)
	if err != nil || recovered == nil || recovered.Code != "runtime_confirmed" {
		t.Fatalf("original attempt recovery: %+v %v", recovered, err)
	}
	if trace := maintenanceRequests(); len(trace) != beforeRecovery+1 || trace[len(trace)-1] {
		t.Fatalf("recovery acquired a new lease: %v", trace)
	}

	// Replace a donor during an explicit drained apply, then reject its native
	// config. The real executor/helper must settle the attempt, not strand GB.
	egressSnapshot, _ = egressStore.Snapshot()
	egressSnapshot.Config.Profiles["donor"] = egressconfig.Profile{Name: "Donor fixture", Type: "cellular_sim", SIMICCID: "8985200000000000001"}
	egressSnapshot.Config.Exits["hk"] = egressconfig.Exit{Enabled: true, ProfileID: "donor"}
	egressSnapshot, err = egressStore.PutExpected(egressSnapshot.Config, egressSnapshot.Revision)
	if err != nil {
		t.Fatal(err)
	}
	result, err = restarted.ApplyEgress(t.Context(), egressSnapshot.Revision, catalogSnapshot.Revision)
	if err != nil || result.State != "applied" {
		t.Fatal("initial donor did not apply", result, err)
	}
	egressSnapshot.Config.Profiles["donor"] = egressconfig.Profile{Name: "Donor fixture", Type: "cellular_sim", SIMICCID: "8985200000000000002"}
	egressSnapshot, err = egressStore.PutExpected(egressSnapshot.Config, egressSnapshot.Revision)
	if err != nil {
		t.Fatal(err)
	}
	rejectFile := filepath.Join(directory, "reject-check")
	if err := os.WriteFile(rejectFile, []byte("reject native check"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = restarted.ApplyEgress(t.Context(), egressSnapshot.Revision, catalogSnapshot.Revision)
	if err != nil || result.State != "unavailable" {
		t.Fatal("rejected donor attempt did not finish fail closed", result, err)
	}
	confirmed, err := egressdesired.ReadRuntime(statusPath)
	if err != nil || confirmed.Outcome != "unavailable" || confirmed.Exits["gb"].Ready || confirmed.Exits["hk"].ProxyPort != 0 {
		t.Fatal("rejected candidate lacks stopped confirmation", confirmed, err)
	}
	journal, err := restarted.readEgressAttempt()
	maintenanceMu.Lock()
	retainedLease := maintenanceLease
	maintenanceMu.Unlock()
	if err != nil || journal.Phase != "complete" || retainedLease != "" {
		t.Fatal("GB maintenance retained after confirmed stop", journal, err)
	}
	again := &providerApplyService{settings: settings, uid: os.Getuid(), gid: os.Getgid(), desiredUID: os.Getuid()}
	beforeResume := len(maintenanceRequests())
	if _, err := again.recoverEgressAttempt(t.Context(), server.URL); err != nil || len(maintenanceRequests()) != beforeResume {
		t.Fatal("restart repeated completed rejection cleanup", err)
	}
	if err := os.Remove(rejectFile); err != nil {
		t.Fatal(err)
	}
	result, err = again.ApplyEgress(t.Context(), egressSnapshot.Revision, catalogSnapshot.Revision)
	if err != nil || result.State != "applied" {
		t.Fatal("rejected old attempt blocked subsequent explicit apply", result, err)
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

func TestEgressMaintenanceReleasesHealthySubsetAndRetainsUnknownOwner(t *testing.T) {
	root := t.TempDir()
	settings := config{}
	settings.ProviderApply.ReceiptPath = root
	settings.Local.Token = strings.Repeat("t", 32)
	service := &providerApplyService{settings: settings}
	lease := "egress-original"
	attempt := &egressAttempt{Document: egressdesired.Document{AttemptID: lease}, Phase: "confirmed",
		Drain: providerapply.DrainRequest{SchemaVersion: 1, CatalogRevision: 2, LeaseID: lease, LineIDs: []string{"gb", "hk"},
			ExpectedGenerations: map[string]string{"gb": "gb-original", "hk": "hk-original"}}}
	var mu sync.Mutex
	var resumed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case providerapply.Path:
			_ = json.NewEncoder(w).Encode(providerapply.Snapshot{SchemaVersion: 1, CatalogRevision: 3, Lines: []providerapply.LineStatus{
				{LineID: "gb", ProviderPresent: true, Code: "provider_reachable", ProcessGeneration: "gb-original", Maintenance: vowifiipc.MaintenanceStatus{Draining: true, LeaseID: lease}},
				{LineID: "hk", ProviderPresent: true, Code: "provider_reachable", ProcessGeneration: "hk-replacement", Maintenance: vowifiipc.MaintenanceStatus{Draining: true, LeaseID: "foreign-lease"}},
			}})
		case providerapply.ResumePath:
			var input providerapply.DrainRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				w.WriteHeader(400)
				return
			}
			result := providerapply.DrainResult{SchemaVersion: 1, CatalogRevision: input.CatalogRevision, LeaseID: input.LeaseID, Ready: true, Code: "resumed"}
			for _, id := range input.LineIDs {
				resumed = append(resumed, id)
				result.Lines = append(result.Lines, providerapply.DrainLineResult{LineID: id, Code: "resumed", ProcessGeneration: input.ExpectedGenerations[id]})
			}
			_ = json.NewEncoder(w).Encode(result)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	if err := service.releaseEgressAttempt(t.Context(), server.URL, attempt); err == nil {
		t.Fatal("replacement generation was considered cleaned")
	}
	mu.Lock()
	received := append([]string(nil), resumed...)
	mu.Unlock()
	if len(received) != 1 || received[0] != "gb" {
		t.Fatal("unknown sibling stranded healthy maintenance", received)
	}
	restarted := &providerApplyService{settings: settings}
	stored, err := restarted.readEgressAttempt()
	if err != nil || stored.Phase == "complete" || len(stored.Drain.LineIDs) != 1 || stored.Drain.LineIDs[0] != "hk" || stored.Drain.ExpectedGenerations["hk"] != "hk-original" {
		t.Fatal("remaining original owner was not persisted", stored, err)
	}
	if _, err := restarted.recoverEgressAttempt(t.Context(), server.URL); err == nil {
		t.Fatal("unknown owner lost on helper restart")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resumed) != 1 {
		t.Fatal("recovery resumed foreign owner or repeated completed cleanup", resumed)
	}
}
