//go:build !windows

package releaseinstall

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
		filepath.Join(layout.LibexecDirectory, "mdd-core"):         filepath.Join(layout.CurrentLink, "mdd-core"),
		filepath.Join(layout.LibexecDirectory, "mdd-agent"):        filepath.Join(layout.CurrentLink, "mdd-agent"),
		filepath.Join(layout.UnitDirectory, "mdd-core.service"):    filepath.Join(layout.CurrentLink, "mdd-core.service"),
		filepath.Join(layout.UnitDirectory, "mdd-agent.service"):   filepath.Join(layout.CurrentLink, "mdd-agent.service"),
		filepath.Join(layout.LibexecDirectory, "mdd-vowifi"):       filepath.Join(layout.CurrentLink, "mdd-vowifi"),
		filepath.Join(layout.UnitDirectory, "mdd-vowifi@.service"): filepath.Join(layout.CurrentLink, "mdd-vowifi@.service"),
		filepath.Join(layout.UnitDirectory, "mdd-egress.service"):  filepath.Join(layout.CurrentLink, "mdd-egress.service"),
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
		{"mdd-vowifi", releasebundle.RoleProvider, 0o755},
		{"mdd-core.service", releasebundle.RoleCoreUnit, 0o644}, {"mdd-agent.service", releasebundle.RoleAgentUnit, 0o644},
		{"mdd-vowifi@.service", releasebundle.RoleProviderUnit, 0o644},
		{"mdd-egress.service", releasebundle.RoleEgressUnit, 0o644},
		{"mdd-vowifi-source.tar.gz", releasebundle.RoleProviderSource, 0o644}, {"LICENSE-NOTICE.md", releasebundle.RoleProviderNotice, 0o644},
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
