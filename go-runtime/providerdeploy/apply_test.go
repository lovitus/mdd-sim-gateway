package providerdeploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type fakeServiceManager struct {
	active, enabled map[string]bool
	maintenance     *fakeMaintenance
	unitLines       map[string]string
	failStart       string
	actions         []string
}

func (manager *fakeServiceManager) IsActive(_ context.Context, unit string) (bool, error) {
	return manager.active[unit], nil
}
func (manager *fakeServiceManager) IsEnabled(_ context.Context, unit string) (bool, error) {
	return manager.enabled[unit], nil
}
func (manager *fakeServiceManager) Start(_ context.Context, unit string) error {
	manager.actions = append(manager.actions, "start "+unit)
	if manager.failStart == unit {
		manager.failStart = ""
		return errors.New("injected start failure")
	}
	manager.active[unit] = true
	manager.maintenance.present[manager.unitLines[unit]] = true
	return nil
}
func (manager *fakeServiceManager) Stop(_ context.Context, unit string) error {
	manager.actions = append(manager.actions, "stop "+unit)
	manager.active[unit] = false
	manager.maintenance.present[manager.unitLines[unit]] = false
	return nil
}
func (manager *fakeServiceManager) Enable(_ context.Context, unit string) error {
	manager.actions = append(manager.actions, "enable "+unit)
	manager.enabled[unit] = true
	return nil
}
func (manager *fakeServiceManager) Disable(_ context.Context, unit string) error {
	manager.actions = append(manager.actions, "disable "+unit)
	manager.enabled[unit] = false
	return nil
}

type fakeMaintenance struct {
	revision          uint64
	present           map[string]bool
	drains            map[string]string
	failResume        bool
	rejectAbsentDrain bool
}

func (maintenance *fakeMaintenance) Request(_ context.Context, request providerapply.DrainRequest, begin bool) (providerapply.DrainResult, error) {
	result := providerapply.DrainResult{SchemaVersion: 1, CatalogRevision: request.CatalogRevision, LeaseID: request.LeaseID}
	if !begin && maintenance.failResume {
		return result, errors.New("injected resume failure")
	}
	for _, lineID := range request.LineIDs {
		if begin {
			if maintenance.rejectAbsentDrain && !maintenance.present[lineID] {
				return result, errors.New("absent provider cannot drain")
			}
			maintenance.drains[lineID] = request.LeaseID
		} else if maintenance.drains[lineID] == request.LeaseID {
			delete(maintenance.drains, lineID)
		}
	}
	return result, nil
}

func (maintenance *fakeMaintenance) Snapshot(context.Context) (providerapply.Snapshot, error) {
	snapshot := providerapply.Snapshot{SchemaVersion: 1, CatalogRevision: maintenance.revision}
	for _, lineID := range []string{"line-added", "line-changed", "line-removed"} {
		status := providerapply.LineStatus{LineID: lineID, Code: "provider_absent"}
		if maintenance.present[lineID] {
			status.Code, status.ProviderPresent = "provider_reachable", true
		}
		if lease := maintenance.drains[lineID]; lease != "" {
			status.Maintenance = vowifiipc.MaintenanceStatus{Draining: true, LeaseID: lease}
		}
		snapshot.Lines = append(snapshot.Lines, status)
	}
	return snapshot, nil
}

func TestExecuteLockedAppliesChangedRemovedAndAddedProviders(t *testing.T) {
	input, manager, maintenance := applyFixture(t)
	receipt, err := executeLocked(context.Background(), input)
	if err != nil || receipt.State != StateApplied {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if target, err := CurrentTarget(input.CurrentLink); err != nil || target != input.CandidateTarget {
		t.Fatalf("target=%q err=%v", target, err)
	}
	if manager.active[unitForLine(input.Current, "line-removed")] || manager.enabled[unitForLine(input.Current, "line-removed")] ||
		!manager.active[unitForLine(input.Candidate, "line-changed")] || !manager.active[unitForLine(input.Candidate, "line-added")] {
		t.Fatalf("active=%v enabled=%v", manager.active, manager.enabled)
	}
	if len(maintenance.drains) != 1 || maintenance.drains["line-removed"] == "" {
		t.Fatalf("unexpected retained drains: %v", maintenance.drains)
	}
}

func TestExecuteLockedRollsBackBeforeCommit(t *testing.T) {
	input, manager, maintenance := applyFixture(t)
	manager.failStart = unitForLine(input.Candidate, "line-added")
	receipt, err := executeLocked(context.Background(), input)
	if err == nil || receipt.State != StateRolledBack {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if target, targetErr := CurrentTarget(input.CurrentLink); targetErr != nil || target != input.PreviousTarget {
		t.Fatalf("target=%q err=%v", target, targetErr)
	}
	if !manager.active[unitForLine(input.Current, "line-changed")] || !manager.active[unitForLine(input.Current, "line-removed")] ||
		manager.active[unitForLine(input.Candidate, "line-added")] || manager.enabled[unitForLine(input.Candidate, "line-added")] || len(maintenance.drains) != 0 {
		t.Fatalf("active=%v enabled=%v drains=%v", manager.active, manager.enabled, maintenance.drains)
	}
}

func TestExecuteLockedLeavesCommittedCandidateWhenResumeFails(t *testing.T) {
	input, _, maintenance := applyFixture(t)
	maintenance.failResume = true
	receipt, err := executeLocked(context.Background(), input)
	if err == nil || receipt.State != StateAppliedResumeIncomplete {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if target, targetErr := CurrentTarget(input.CurrentLink); targetErr != nil || target != input.CandidateTarget {
		t.Fatalf("target=%q err=%v", target, targetErr)
	}
	if _, openErr := OpenJournal(input.ReceiptDirectory, input.PreviousTarget, input.CandidateTarget, "another-lease", input.Plan); !errors.Is(openErr, ErrIncompleteReceipt) {
		t.Fatalf("next apply was not blocked: %v", openErr)
	}
}

func TestExecuteLockedDoesNotDrainAlreadyStoppedProviders(t *testing.T) {
	input, manager, maintenance := applyFixture(t)
	maintenance.rejectAbsentDrain = true
	for _, lineID := range []string{"line-changed", "line-removed"} {
		unit := unitForLine(input.Current, lineID)
		manager.active[unit] = false
		maintenance.present[lineID] = false
	}
	input.Preflight, _ = maintenance.Snapshot(context.Background())
	input.Plan = providerapply.BuildPlan(input.Current, input.Candidate, input.Preflight)
	receipt, err := executeLocked(context.Background(), input)
	if err != nil || receipt.State != StateApplied {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if len(maintenance.drains) != 0 {
		t.Fatalf("stopped providers acquired drains: %v", maintenance.drains)
	}
}

func applyFixture(t *testing.T) (ApplyInput, *fakeServiceManager, *fakeMaintenance) {
	t.Helper()
	root := t.TempDir()
	current := writeProviderSet(t, filepath.Join(root, "old"), 7, map[string]string{
		"line-changed": "old", "line-removed": "old",
	})
	candidate := writeProviderSet(t, filepath.Join(root, "new"), 8, map[string]string{
		"line-changed": "new", "line-added": "new",
	})
	link := filepath.Join(root, "current")
	if err := SwitchLink(link, filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	receipts := filepath.Join(root, "receipts")
	if err := os.Mkdir(receipts, 0o700); err != nil {
		t.Fatal(err)
	}
	maintenance := &fakeMaintenance{revision: 8, present: map[string]bool{
		"line-changed": true, "line-removed": true,
	}, drains: map[string]string{}}
	manager := &fakeServiceManager{active: map[string]bool{}, enabled: map[string]bool{}, maintenance: maintenance, unitLines: map[string]string{}}
	preflight, _ := maintenance.Snapshot(context.Background())
	plan := providerapply.BuildPlan(current, candidate, preflight)
	if !plan.Safe || len(plan.Added) != 1 || len(plan.Changed) != 1 || len(plan.Removed) != 1 {
		t.Fatalf("plan=%+v", plan)
	}
	for _, change := range append(append(append([]providerapply.Change{}, plan.Added...), plan.Changed...), plan.Removed...) {
		manager.unitLines[unitFor(change.UnitInstance)] = change.LineID
	}
	for _, lineID := range []string{"line-changed", "line-removed"} {
		unit := unitForLine(current, lineID)
		manager.active[unit], manager.enabled[unit] = true, true
	}
	return ApplyInput{
		CurrentLink: link, PreviousTarget: filepath.Join(root, "old"), CandidateTarget: filepath.Join(root, "new"),
		ReceiptDirectory: receipts, Candidate: candidate, Current: current, Plan: plan, Preflight: preflight,
		Manager: manager, Maintenance: maintenance,
	}, manager, maintenance
}

func writeProviderSet(t *testing.T, directory string, revision uint64, lines map[string]string) providerconfig.Manifest {
	t.Helper()
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := providerconfig.Manifest{SchemaVersion: 1, CatalogRevision: revision, Providers: []providerconfig.ManifestEntry{}}
	for _, lineID := range []string{"line-added", "line-changed", "line-removed"} {
		marker, found := lines[lineID]
		if !found {
			continue
		}
		instance := providerconfig.UnitInstance(lineID)
		var settings providerconfig.Config
		settings.LineID, settings.ProviderID, settings.DeviceID = lineID, "native-"+marker, "device-"+lineID
		settings.IPC.Listen, settings.IPC.Token = "127.0.0.1:0", strings.Repeat("i", 32)
		settings.IPC.StatePath = filepath.Join(directory, lineID+".db")
		settings.Agent.BrokerToken = strings.Repeat("a", 32)
		payload, _ := json.Marshal(settings)
		payload = append(payload, '\n')
		name := instance + ".json"
		if err := os.WriteFile(filepath.Join(directory, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(payload)
		manifest.Providers = append(manifest.Providers, providerconfig.ManifestEntry{
			LineID: lineID, UnitInstance: instance, ConfigFile: name, ConfigSHA256: hex.EncodeToString(digest[:]),
		})
	}
	payload, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := providerconfig.LoadDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func unitForLine(manifest providerconfig.Manifest, lineID string) string {
	return unitFor(candidateEntry(manifest, lineID).UnitInstance)
}
