//go:build !windows

package releaseinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRemovalGate struct {
	calls      int
	failOnCall int
}

func (gate *fakeRemovalGate) VerifyInactiveDisabled(context.Context) error {
	gate.calls++
	if gate.failOnCall == gate.calls {
		return errors.New("unit state changed")
	}
	return nil
}

func TestInspectRemoveAcceptsStrictTerminalInstallation(t *testing.T) {
	layout, identity, current := removalFixture(t)
	inspection, err := inspectRemove(layout, identity.RootUID, identity.RootGID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.plan.SchemaVersion != 1 || inspection.plan.CurrentRelease != current ||
		len(inspection.plan.ReleaseIDs) != 2 || len(inspection.plan.StableLinks) != 10 {
		t.Fatalf("inspection=%+v", inspection)
	}
}

func TestRemoveLockedDeletesOnlyVerifiedSoftwareAndPreservesState(t *testing.T) {
	layout, identity, _ := removalFixture(t)
	inspection, err := inspectRemove(layout, identity.RootUID, identity.RootGID)
	if err != nil {
		t.Fatal(err)
	}
	configEvidence := filepath.Join(layout.ConfigDirectory, "keep.json")
	stateEvidence := filepath.Join(layout.StateDirectory, "keep.db")
	if err := os.WriteFile(configEvidence, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateEvidence, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(layout.ReceiptDirectory, "current.json")
	receiptBefore, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	priorReceipt, err := decodeReceipt(receiptBefore)
	if err != nil {
		t.Fatal(err)
	}
	removeUnitLinks(t, layout, inspection)
	if _, err := inspectRemove(layout, identity.RootUID, identity.RootGID); err == nil {
		t.Fatal("strict preflight accepted unit links removed by systemctl disable")
	}
	gate, reloader := &fakeRemovalGate{}, &fakeReloader{}
	plan, err := removeLocked(context.Background(), layout, identity.RootUID, identity.RootGID, gate, reloader)
	if err != nil || plan.SchemaVersion != 1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if gate.calls != 2 || reloader.calls != 1 {
		t.Fatalf("gate calls=%d reloads=%d", gate.calls, reloader.calls)
	}
	for _, path := range []string{layout.CurrentLink, layout.ReleasesDirectory, layout.LibexecDirectory, filepath.Dir(layout.ReleasesDirectory)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("software path remains: %s err=%v", path, err)
		}
	}
	for _, path := range plan.StableLinks {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stable link remains: %s err=%v", path, err)
		}
	}
	for path, want := range map[string]string{configEvidence: "config", stateEvidence: "state"} {
		payload, err := os.ReadFile(path)
		if err != nil || string(payload) != want {
			t.Fatalf("preserved path=%s payload=%q err=%v", path, payload, err)
		}
	}
	receiptAfter, err := os.ReadFile(receiptPath)
	if err != nil || string(receiptAfter) != string(receiptBefore) {
		t.Fatalf("receipt changed err=%v", err)
	}

	thirdSource, third := testBundle(t, filepath.Join(t.TempDir(), "third"), "release-remove-003", "c")
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	installed, err := activateLocked(context.Background(), thirdSource, third, layout, identity, &fakeReloader{})
	if err != nil || installed.State != StateApplied {
		t.Fatalf("reinstall receipt=%+v err=%v", installed, err)
	}
	wantCurrent := filepath.Join(layout.ReleasesDirectory, third.ReleaseID)
	if current, err := currentTarget(layout.CurrentLink); err != nil || current != wantCurrent {
		t.Fatalf("reinstalled current=%q want=%q err=%v", current, wantCurrent, err)
	}
	archivePath := filepath.Join(layout.ReceiptDirectory, priorReceipt.ReceiptID+".json")
	archived, err := os.ReadFile(archivePath)
	if err != nil || string(archived) != string(receiptBefore) {
		t.Fatalf("preserved receipt was not archived on reinstall: err=%v", err)
	}
}

func removeUnitLinks(t *testing.T, layout Layout, inspection removeInspection) {
	t.Helper()
	for _, path := range inspection.plan.StableLinks {
		if filepath.Dir(path) != layout.UnitDirectory {
			continue
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetachAndReloadRestoresLinksWhenDetachFailsMidway(t *testing.T) {
	layout, identity, _ := removalFixture(t)
	before, err := inspectRemove(layout, identity.RootUID, identity.RootGID)
	if err != nil {
		t.Fatal(err)
	}
	removeCalls := 0
	remove := func(path string) error {
		removeCalls++
		if removeCalls == 2 {
			return errors.New("injected detach failure")
		}
		return os.Remove(path)
	}
	gate, reloader := &fakeRemovalGate{}, &fakeReloader{}
	if err := detachAndReloadWithRemove(context.Background(), layout, before, gate, reloader, remove); err == nil {
		t.Fatal("partial detach failure was accepted")
	}
	if removeCalls != 2 || gate.calls != 0 || reloader.calls != 0 {
		t.Fatalf("remove calls=%d gate calls=%d reloads=%d", removeCalls, gate.calls, reloader.calls)
	}
	assertRemovalLinks(t, layout, before)
	for _, path := range before.releases {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("release was removed after partial detach: %s err=%v", path, err)
		}
	}
}

func TestRemoveLockedRestoresLinksWhenDaemonReloadFails(t *testing.T) {
	layout, identity, _ := removalFixture(t)
	before, err := inspectRemove(layout, identity.RootUID, identity.RootGID)
	if err != nil {
		t.Fatal(err)
	}
	gate, reloader := &fakeRemovalGate{}, &fakeReloader{failures: 1}
	if _, err := removeLocked(context.Background(), layout, identity.RootUID, identity.RootGID, gate, reloader); err == nil {
		t.Fatal("reload failure was accepted")
	}
	if gate.calls != 1 || reloader.calls != 2 {
		t.Fatalf("gate calls=%d reloads=%d", gate.calls, reloader.calls)
	}
	assertRemovalLinks(t, layout, before)
	for _, path := range before.releases {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("release was removed after rollback: %s err=%v", path, err)
		}
	}
}

func TestRemoveLockedRestoresLinksWhenPostReloadGateChanges(t *testing.T) {
	layout, identity, _ := removalFixture(t)
	before, err := inspectRemove(layout, identity.RootUID, identity.RootGID)
	if err != nil {
		t.Fatal(err)
	}
	gate, reloader := &fakeRemovalGate{failOnCall: 2}, &fakeReloader{}
	if _, err := removeLocked(context.Background(), layout, identity.RootUID, identity.RootGID, gate, reloader); err == nil {
		t.Fatal("post-reload unit race was accepted")
	}
	if gate.calls != 2 || reloader.calls != 2 {
		t.Fatalf("gate calls=%d reloads=%d", gate.calls, reloader.calls)
	}
	assertRemovalLinks(t, layout, before)
}

func TestRemoveLockedInitialGateFailureHasNoFilesystemSideEffects(t *testing.T) {
	layout, identity, _ := removalFixture(t)
	before, err := inspectRemove(layout, identity.RootUID, identity.RootGID)
	if err != nil {
		t.Fatal(err)
	}
	gate, reloader := &fakeRemovalGate{failOnCall: 1}, &fakeReloader{}
	if _, err := removeLocked(context.Background(), layout, identity.RootUID, identity.RootGID, gate, reloader); err == nil {
		t.Fatal("active unit proof was accepted")
	}
	if gate.calls != 1 || reloader.calls != 0 {
		t.Fatalf("gate calls=%d reloads=%d", gate.calls, reloader.calls)
	}
	assertRemovalLinks(t, layout, before)
}

func TestInspectRemoveAfterDisableOnlyAllowsKnownUnitLinksToBeAbsent(t *testing.T) {
	layout, identity, _ := removalFixture(t)
	inspection, err := inspectRemove(layout, identity.RootUID, identity.RootGID)
	if err != nil {
		t.Fatal(err)
	}
	removeUnitLinks(t, layout, inspection)
	if _, err := inspectRemoveAfterDisable(layout, identity.RootUID, identity.RootGID); err != nil {
		t.Fatalf("disabled unit links were rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(layout.LibexecDirectory, "mdd-core")); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRemoveAfterDisable(layout, identity.RootUID, identity.RootGID); err == nil {
		t.Fatal("missing executable link was accepted after disable")
	}
}

func TestInspectRemoveRejectsTamperedManagedState(t *testing.T) {
	tests := map[string]func(t *testing.T, layout Layout, inspection removeInspection){
		"receipt mode": func(t *testing.T, layout Layout, _ removeInspection) {
			if err := os.Chmod(filepath.Join(layout.ReceiptDirectory, "current.json"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"stable target": func(t *testing.T, layout Layout, inspection removeInspection) {
			path := inspection.plan.StableLinks[0]
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/unmanaged", path); err != nil {
				t.Fatal(err)
			}
		},
		"extra libexec": func(t *testing.T, layout Layout, _ removeInspection) {
			if err := os.WriteFile(filepath.Join(layout.LibexecDirectory, "foreign"), []byte("x"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"release payload": func(t *testing.T, _ Layout, inspection removeInspection) {
			if err := os.WriteFile(filepath.Join(inspection.current, "mdd-core"), []byte("changed"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"extra release entry": func(t *testing.T, layout Layout, _ removeInspection) {
			if err := os.WriteFile(filepath.Join(layout.ReleasesDirectory, "foreign"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			layout, identity, _ := removalFixture(t)
			before, err := inspectRemove(layout, identity.RootUID, identity.RootGID)
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, layout, before)
			if _, err := inspectRemove(layout, identity.RootUID, identity.RootGID); err == nil {
				t.Fatal("tampered removal state was accepted")
			} else if name == "stable target" && !strings.Contains(err.Error(), before.plan.StableLinks[0]) {
				t.Fatalf("stable-link error omitted path: %v", err)
			}
		})
	}
}

func removalFixture(t *testing.T) (Layout, Identity, string) {
	t.Helper()
	layout, identity := testLayout(t)
	firstSource, first := testBundle(t, filepath.Join(t.TempDir(), "first"), "release-remove-001", "a")
	secondSource, second := testBundle(t, filepath.Join(t.TempDir(), "second"), "release-remove-002", "b")
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := activateLocked(context.Background(), firstSource, first, layout, identity, &fakeReloader{}); err != nil {
		t.Fatal(err)
	}
	if _, err := activateLocked(context.Background(), secondSource, second, layout, identity, &fakeReloader{}); err != nil {
		t.Fatal(err)
	}
	return layout, identity, second.ReleaseID
}

func assertRemovalLinks(t *testing.T, layout Layout, inspection removeInspection) {
	t.Helper()
	if got, err := os.Readlink(layout.CurrentLink); err != nil || got != inspection.current {
		t.Fatalf("current=%q err=%v", got, err)
	}
	for path, want := range inspection.stableTarget {
		if got, err := os.Readlink(path); err != nil || got != want {
			t.Fatalf("link=%s got=%q want=%q err=%v", path, got, want, err)
		}
	}
}
