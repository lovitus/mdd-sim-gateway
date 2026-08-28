//go:build linux

package providerdeploy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type systemdMaintenance struct {
	revision uint64
	manager  Systemctl
	units    map[string]string
	drains   map[string]string
}

func (maintenance *systemdMaintenance) Request(_ context.Context, request providerapply.DrainRequest, begin bool) (providerapply.DrainResult, error) {
	for _, lineID := range request.LineIDs {
		if begin {
			maintenance.drains[lineID] = request.LeaseID
		} else if maintenance.drains[lineID] == request.LeaseID {
			delete(maintenance.drains, lineID)
		}
	}
	return providerapply.DrainResult{SchemaVersion: 1, CatalogRevision: request.CatalogRevision, LeaseID: request.LeaseID, Ready: true, Code: "ready"}, nil
}

func (maintenance *systemdMaintenance) Snapshot(ctx context.Context) (providerapply.Snapshot, error) {
	snapshot := providerapply.Snapshot{SchemaVersion: 1, CatalogRevision: maintenance.revision}
	for _, lineID := range []string{"line-added", "line-changed", "line-removed"} {
		active, err := maintenance.manager.IsActive(ctx, maintenance.units[lineID])
		if err != nil {
			return snapshot, err
		}
		status := providerapply.LineStatus{LineID: lineID, Code: "provider_absent"}
		if active {
			status.Code, status.ProviderPresent = "provider_reachable", true
		}
		if lease := maintenance.drains[lineID]; lease != "" {
			status.Maintenance = vowifiipc.MaintenanceStatus{Draining: true, LeaseID: lease}
		}
		snapshot.Lines = append(snapshot.Lines, status)
	}
	return snapshot, nil
}

func TestExecuteUsesRealSystemdWhenExplicitlyEnabled(t *testing.T) {
	root := os.Getenv("MDD_PROVIDER_APPLY_SYSTEMD_ROOT")
	if root == "" {
		t.Skip("real systemd integration is opt-in")
	}
	manager := Systemctl{Path: "/bin/systemctl"}
	if err := manager.Validate(); err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(root, "old")
	candidatePath := filepath.Join(root, "new")
	current := writeProviderSet(t, currentPath, 7, map[string]string{"line-changed": "old", "line-removed": "old"})
	candidate := writeProviderSet(t, candidatePath, 8, map[string]string{"line-changed": "new", "line-added": "new"})
	units := map[string]string{}
	for _, entry := range append(append([]providerconfig.ManifestEntry{}, current.Providers...), candidate.Providers...) {
		units[entry.LineID] = unitFor(entry.UnitInstance)
	}
	ctx := context.Background()
	for _, unit := range units {
		unit := unit
		t.Cleanup(func() {
			_ = manager.Stop(ctx, unit)
			_ = manager.Disable(ctx, unit)
		})
	}
	for _, lineID := range []string{"line-changed", "line-removed"} {
		if err := manager.Enable(ctx, units[lineID]); err != nil {
			t.Fatal(err)
		}
		if err := manager.Start(ctx, units[lineID]); err != nil {
			t.Fatal(err)
		}
	}
	maintenance := &systemdMaintenance{revision: 8, manager: manager, units: units, drains: map[string]string{}}
	preflight, err := maintenance.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan := providerapply.BuildPlan(current, candidate, preflight)
	if !plan.Safe {
		t.Fatalf("plan=%+v", plan)
	}
	link := filepath.Join(root, "current")
	if err := SwitchLink(link, currentPath); err != nil {
		t.Fatal(err)
	}
	receipts := filepath.Join(root, "receipts")
	if err := os.Mkdir(receipts, 0o700); err != nil {
		t.Fatal(err)
	}
	receipt, err := Execute(ctx, ApplyInput{
		CurrentLink: link, PreviousTarget: currentPath, CandidateTarget: candidatePath,
		ReceiptDirectory: receipts, Current: current, Candidate: candidate, Plan: plan, Preflight: preflight,
		Manager: manager, Maintenance: maintenance,
	})
	if err != nil || receipt.State != StateApplied {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if active, _ := manager.IsActive(ctx, units["line-removed"]); active {
		t.Fatal("removed provider is still active")
	}
	for _, lineID := range []string{"line-changed", "line-added"} {
		if active, activeErr := manager.IsActive(ctx, units[lineID]); activeErr != nil || !active {
			t.Fatalf("%s active=%v err=%v", lineID, active, activeErr)
		}
	}
}
