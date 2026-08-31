//go:build !windows

package releaseinstall

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
)

type fakeReloader struct {
	calls    int
	failures int
}

func (reloader *fakeReloader) DaemonReload() error {
	reloader.calls++
	if reloader.failures > 0 {
		reloader.failures--
		return os.ErrInvalid
	}
	return nil
}

func TestActivateLockedInstallsUpgradesAndRollsBackWithoutStartingServices(t *testing.T) {
	layout, identity := testLayout(t)
	firstSource, first := testBundle(t, filepath.Join(t.TempDir(), "first"), "release-001", "a")
	secondSource, second := testBundle(t, filepath.Join(t.TempDir(), "second"), "release-002", "b")
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	reloader := &fakeReloader{}
	for _, item := range []struct {
		source   string
		manifest releasebundle.Manifest
	}{
		{firstSource, first}, {secondSource, second}, {firstSource, first},
	} {
		receipt, err := activateLocked(context.Background(), item.source, item.manifest, layout, identity, reloader)
		if err != nil || receipt.State != StateApplied {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
		want := filepath.Join(layout.ReleasesDirectory, item.manifest.ReleaseID)
		if got, err := currentTarget(layout.CurrentLink); err != nil || got != want {
			t.Fatalf("current=%q want=%q err=%v", got, want, err)
		}
	}
	if reloader.calls != 3 {
		t.Fatalf("daemon reload calls=%d", reloader.calls)
	}
	for link, target := range map[string]string{
		filepath.Join(layout.LibexecDirectory, "mdd-core"):                filepath.Join(layout.CurrentLink, "mdd-core"),
		filepath.Join(layout.LibexecDirectory, "mdd-agent"):               filepath.Join(layout.CurrentLink, "mdd-agent"),
		filepath.Join(layout.LibexecDirectory, "mdd-call-audio-helper"):   filepath.Join(layout.CurrentLink, "mdd-call-audio-helper"),
		filepath.Join(layout.UnitDirectory, "mdd-core.service"):           filepath.Join(layout.CurrentLink, "mdd-core.service"),
		filepath.Join(layout.UnitDirectory, "mdd-agent.service"):          filepath.Join(layout.CurrentLink, "mdd-agent.service"),
		filepath.Join(layout.UnitDirectory, "mdd-cellular-guard.service"): filepath.Join(layout.CurrentLink, "mdd-cellular-guard.service"),
		filepath.Join(layout.LibexecDirectory, "mdd-vowifi"):              filepath.Join(layout.CurrentLink, "mdd-vowifi"),
		filepath.Join(layout.UnitDirectory, "mdd-vowifi@.service"):        filepath.Join(layout.CurrentLink, "mdd-vowifi@.service"),
		filepath.Join(layout.UnitDirectory, "mdd-provider-apply.service"): filepath.Join(layout.CurrentLink, "mdd-provider-apply.service"),
		filepath.Join(layout.UnitDirectory, "mdd-egress.service"):         filepath.Join(layout.CurrentLink, "mdd-egress.service"),
	} {
		if got, err := os.Readlink(link); err != nil || got != target {
			t.Fatalf("link=%s target=%q err=%v", link, got, err)
		}
	}
}

func TestActivateLockedRestoresPreviousReleaseWhenReloadFails(t *testing.T) {
	layout, identity := testLayout(t)
	firstSource, first := testBundle(t, filepath.Join(t.TempDir(), "first"), "release-001", "a")
	secondSource, second := testBundle(t, filepath.Join(t.TempDir(), "second"), "release-002", "b")
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := activateLocked(context.Background(), firstSource, first, layout, identity, &fakeReloader{}); err != nil {
		t.Fatal(err)
	}
	reloader := &fakeReloader{failures: 1}
	receipt, err := activateLocked(context.Background(), secondSource, second, layout, identity, reloader)
	if err == nil || receipt.State != StateRolledBack {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	want := filepath.Join(layout.ReleasesDirectory, first.ReleaseID)
	if got, targetErr := currentTarget(layout.CurrentLink); targetErr != nil || got != want {
		t.Fatalf("current=%q want=%q err=%v", got, want, targetErr)
	}
}

func TestActivateLockedRemovesNewStableLinksWhenInitialReloadFails(t *testing.T) {
	layout, identity := testLayout(t)
	source, manifest := testBundle(t, filepath.Join(t.TempDir(), "first"), "release-links", "c")
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	receipt, err := activateLocked(context.Background(), source, manifest, layout, identity, &fakeReloader{failures: 1})
	if err == nil || receipt.State != StateRolledBack {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if current, currentErr := currentTarget(layout.CurrentLink); currentErr != nil || current != "" {
		t.Fatalf("current=%q err=%v", current, currentErr)
	}
	for _, path := range []string{
		filepath.Join(layout.LibexecDirectory, "mdd-core"),
		filepath.Join(layout.LibexecDirectory, "mdd-agent"),
		filepath.Join(layout.LibexecDirectory, "mdd-call-audio-helper"),
		filepath.Join(layout.LibexecDirectory, "mdd-vowifi"),
		filepath.Join(layout.UnitDirectory, "mdd-core.service"),
		filepath.Join(layout.UnitDirectory, "mdd-agent.service"),
		filepath.Join(layout.UnitDirectory, "mdd-cellular-guard.service"),
		filepath.Join(layout.UnitDirectory, "mdd-vowifi@.service"),
		filepath.Join(layout.UnitDirectory, "mdd-provider-apply.service"),
		filepath.Join(layout.UnitDirectory, "mdd-egress.service"),
	} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("new stable link remains after rollback: %s err=%v", path, statErr)
		}
	}
}

func TestRecoverLockedCompletesManualRollback(t *testing.T) {
	layout, identity := testLayout(t)
	firstSource, first := testBundle(t, filepath.Join(t.TempDir(), "first"), "release-001", "a")
	secondSource, second := testBundle(t, filepath.Join(t.TempDir(), "second"), "release-002", "b")
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := activateLocked(context.Background(), firstSource, first, layout, identity, &fakeReloader{}); err != nil {
		t.Fatal(err)
	}
	receipt, err := activateLocked(context.Background(), secondSource, second, layout, identity, &fakeReloader{failures: 2})
	if err == nil || receipt.State != StateManualRecovery {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	recovered, err := recoverLocked(context.Background(), layout, &fakeReloader{})
	if err != nil || recovered.State != StateRolledBack {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
}

func TestRecoverLockedReconcilesLegacyStableLinksAfterCrash(t *testing.T) {
	layout, identity := testLayout(t)
	legacySource, legacy := testLegacyBundle(t, filepath.Join(t.TempDir(), "legacy"), "release-legacy", "l")
	candidateSource, candidate := testBundle(t, filepath.Join(t.TempDir(), "candidate"), "release-v2", "v")
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := activateLocked(context.Background(), legacySource, legacy, layout, identity, &fakeReloader{}); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(layout.ReleasesDirectory, legacy.ReleaseID)
	target := filepath.Join(layout.ReleasesDirectory, candidate.ReleaseID)
	if err := stageRelease(candidateSource, target, candidate, identity.RootUID, identity.RootGID); err != nil {
		t.Fatal(err)
	}
	if _, err := openJournal(layout.ReceiptDirectory, candidate.ReleaseID, previous, target); err != nil {
		t.Fatal(err)
	}
	if err := reconcileStableLinks(layout, &candidate); err != nil {
		t.Fatal(err)
	}
	if err := switchLink(layout.CurrentLink, target); err != nil {
		t.Fatal(err)
	}
	audioLink := filepath.Join(layout.LibexecDirectory, "mdd-call-audio-helper")
	if _, err := os.Lstat(audioLink); err != nil {
		t.Fatalf("candidate audio link was not created: %v", err)
	}
	receipt, err := recoverLocked(context.Background(), layout, &fakeReloader{})
	if err != nil || receipt.State != StateRolledBack {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if current, err := currentTarget(layout.CurrentLink); err != nil || current != previous {
		t.Fatalf("current=%q want=%q err=%v", current, previous, err)
	}
	for _, removed := range []string{
		audioLink,
		filepath.Join(layout.UnitDirectory, "mdd-cellular-guard.service"),
		filepath.Join(layout.UnitDirectory, "mdd-provider-apply.service"),
		filepath.Join(layout.UnitDirectory, "mdd-egress.service"),
	} {
		if _, err := os.Lstat(removed); !os.IsNotExist(err) {
			t.Fatalf("candidate-only stable link remains after recovery: %s err=%v", removed, err)
		}
	}
}

func TestActivateLockedRejectsUnmanagedStablePath(t *testing.T) {
	layout, identity := testLayout(t)
	source, manifest := testBundle(t, filepath.Join(t.TempDir(), "first"), "release-001", "a")
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(layout.LibexecDirectory, "mdd-core")
	if err := os.WriteFile(stable, []byte("unmanaged"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := activateLocked(context.Background(), source, manifest, layout, identity, &fakeReloader{}); err == nil {
		t.Fatal("unmanaged stable executable was overwritten")
	}
	payload, err := os.ReadFile(stable)
	if err != nil || string(payload) != "unmanaged" {
		t.Fatalf("stable payload=%q err=%v", payload, err)
	}
}

func TestPrepareLayoutAcceptsExistingRootOwnedConfigDirectory(t *testing.T) {
	layout, identity := testLayout(t)
	if err := os.Mkdir(layout.ConfigDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(layout.ConfigDirectory)
	if err != nil {
		t.Fatal(err)
	}
	uid, gid, ok := owner(info)
	if !ok || uid != identity.RootUID || gid != identity.RootGID || info.Mode().Perm() != 0o755 {
		t.Fatalf("config directory owner=%d:%d mode=%#o", uid, gid, info.Mode().Perm())
	}
}

func TestPrepareLayoutCreatesReadOnlyEgressDesiredBoundary(t *testing.T) {
	layout, identity := testLayout(t)
	if err := prepareLayout(layout, identity); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(layout.SystemState), "mdd-egress-config")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	uid, gid, ok := owner(info)
	if !ok || uid != identity.RootUID || gid != identity.ServiceGID || info.Mode().Perm() != 0o750 {
		t.Fatalf("egress config directory owner=%d:%d mode=%#o", uid, gid, info.Mode().Perm())
	}
}

func testLayout(t *testing.T) (Layout, Identity) {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "usr", "lib"), filepath.Join(root, "usr", "libexec"),
		filepath.Join(root, "etc", "systemd", "system"), filepath.Join(root, "var", "lib"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	uid, gid := os.Getuid(), os.Getgid()
	serviceUID, serviceGID := uid, gid
	if serviceUID == 0 {
		serviceUID, serviceGID = 1, 1
	}
	layout := Layout{
		ReleasesDirectory: filepath.Join(root, "usr", "lib", "mdd", "releases"),
		CurrentLink:       filepath.Join(root, "usr", "lib", "mdd", "current"),
		LibexecDirectory:  filepath.Join(root, "usr", "libexec", "mdd"),
		UnitDirectory:     filepath.Join(root, "etc", "systemd", "system"),
		ConfigDirectory:   filepath.Join(root, "etc", "mdd"),
		StateDirectory:    filepath.Join(root, "var", "lib", "mdd"),
		ProviderState:     filepath.Join(root, "var", "lib", "mdd", "providers"),
		SystemState:       filepath.Join(root, "var", "lib", "mdd-system"),
		ReceiptDirectory:  filepath.Join(root, "var", "lib", "mdd-system", "release-install"),
	}
	return layout, Identity{RootUID: uid, RootGID: gid, ServiceUID: serviceUID, ServiceGID: serviceGID}
}

func testBundle(t *testing.T, output, releaseID, marker string) (string, releasebundle.Manifest) {
	t.Helper()
	inputRoot := t.TempDir()
	type item struct {
		name, role string
		mode       os.FileMode
	}
	items := []item{
		{"mdd-core", releasebundle.RoleCore, 0o755}, {"mdd-agent", releasebundle.RoleAgent, 0o755},
		{"mdd-call-audio-helper", releasebundle.RoleAgentAudio, 0o755},
		{"mdd-vowifi", releasebundle.RoleProvider, 0o755},
		{"mdd-core.service", releasebundle.RoleCoreUnit, 0o644}, {"mdd-agent.service", releasebundle.RoleAgentUnit, 0o644},
		{"mdd-cellular-guard.service", releasebundle.RoleGuardUnit, 0o644},
		{"mdd-vowifi@.service", releasebundle.RoleProviderUnit, 0o644},
		{"mdd-provider-apply.service", releasebundle.RoleApplyUnit, 0o644},
		{"mdd-egress.service", releasebundle.RoleEgressUnit, 0o644},
		{"mdd-vowifi-source.tar.gz", releasebundle.RoleProviderSource, 0o644}, {"LICENSE-NOTICE.md", releasebundle.RoleProviderNotice, 0o644},
		{"LICENSE", releasebundle.RoleProjectLicense, 0o644}, {"NOTICE", releasebundle.RoleProjectNotice, 0o644},
		{"THIRD_PARTY_LICENSES.md", releasebundle.RoleThirdParty, 0o644}, {"go-dependency-licenses.tar.gz", releasebundle.RoleGoLicenses, 0o644},
	}
	inputs := make([]releasebundle.Input, 0, len(items))
	for _, item := range items {
		path := filepath.Join(inputRoot, item.name)
		if err := os.WriteFile(path, []byte(item.role+marker), item.mode); err != nil {
			t.Fatal(err)
		}
		input := releasebundle.Input{Name: item.name, Role: item.role, Mode: item.mode, SourcePath: path}
		if item.mode == 0o755 {
			input.GoVersion = runtime.Version()
		}
		inputs = append(inputs, input)
	}
	manifest, err := releasebundle.CreateDirectory(output, releasebundle.Manifest{
		ReleaseID: releaseID, SourceRevision: strings.Repeat(marker, 40), OS: "linux", Architecture: runtime.GOARCH,
	}, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return output, manifest
}

func testLegacyBundle(t *testing.T, output, releaseID, marker string) (string, releasebundle.Manifest) {
	t.Helper()
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	type item struct {
		name, role string
		mode       os.FileMode
	}
	items := []item{
		{"mdd-core", releasebundle.RoleCore, 0o755},
		{"mdd-agent", releasebundle.RoleAgent, 0o755},
		{"mdd-vowifi", releasebundle.RoleProvider, 0o755},
		{"mdd-core.service", releasebundle.RoleCoreUnit, 0o644},
		{"mdd-agent.service", releasebundle.RoleAgentUnit, 0o644},
		{"mdd-vowifi@.service", releasebundle.RoleProviderUnit, 0o644},
		{"mdd-vowifi-source.tar.gz", releasebundle.RoleProviderSource, 0o644},
		{"LICENSE-NOTICE.md", releasebundle.RoleProviderNotice, 0o644},
	}
	manifest := releasebundle.Manifest{
		SchemaVersion: 1, ReleaseID: releaseID, SourceRevision: strings.Repeat(marker, 40),
		OS: "linux", Architecture: runtime.GOARCH,
	}
	for _, item := range items {
		payload := []byte(item.role + marker)
		if err := os.WriteFile(filepath.Join(output, item.name), payload, item.mode); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(payload)
		artifact := releasebundle.Artifact{
			Name: item.name, Role: item.role, Mode: "0644", Size: int64(len(payload)),
			SHA256: fmt.Sprintf("%x", digest[:]),
		}
		if item.mode == 0o755 {
			artifact.Mode, artifact.GoVersion = "0755", runtime.Version()
		}
		manifest.Artifacts = append(manifest.Artifacts, artifact)
	}
	sort.Slice(manifest.Artifacts, func(left, right int) bool {
		return manifest.Artifacts[left].Name < manifest.Artifacts[right].Name
	})
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "manifest.json"), append(payload, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := releasebundle.LoadDirectory(output)
	if err != nil {
		t.Fatal(err)
	}
	return output, loaded
}
