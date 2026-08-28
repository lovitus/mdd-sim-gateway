package releasebundle

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreateAndLoadStrictReleaseDirectory(t *testing.T) {
	root := t.TempDir()
	inputs := testInputs(t, root)
	output := filepath.Join(root, "release")
	manifest, err := CreateDirectory(output, Manifest{
		ReleaseID: "test-001", SourceRevision: strings.Repeat("a", 40), OS: "linux", Architecture: "amd64",
	}, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ReleaseID != "test-001" || len(manifest.Artifacts) != 6 {
		t.Fatalf("manifest=%+v", manifest)
	}
	loaded, err := LoadDirectory(output)
	if err != nil || loaded.ReleaseID != manifest.ReleaseID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("release mode=%v err=%v", info.Mode(), err)
	}
}

func TestReleaseDirectoryRejectsTamperingAndUnexpectedFiles(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "release")
	if _, err := CreateDirectory(output, Manifest{
		ReleaseID: "test-002", SourceRevision: strings.Repeat("b", 40), OS: "linux", Architecture: "arm64",
	}, testInputs(t, root)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "unexpected"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDirectory(output); err == nil {
		t.Fatal("unexpected release file was accepted")
	}
	if err := os.Remove(filepath.Join(output, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "mdd-core"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDirectory(output); err == nil {
		t.Fatal("tampered release binary was accepted")
	}
}

func testInputs(t *testing.T, root string) []Input {
	t.Helper()
	type item struct {
		name, role string
		mode       os.FileMode
	}
	items := []item{
		{"mdd-core", RoleCore, 0o755}, {"mdd-vowifi", RoleProvider, 0o755},
		{"mdd-core.service", RoleCoreUnit, 0o644}, {"mdd-vowifi@.service", RoleProviderUnit, 0o644},
		{"mdd-vowifi-source.tar.gz", RoleProviderSource, 0o644}, {"LICENSE-NOTICE.md", RoleProviderNotice, 0o644},
	}
	result := make([]Input, 0, len(items))
	for _, item := range items {
		path := filepath.Join(root, "input-"+strings.ReplaceAll(item.name, "/", "_"))
		if err := os.WriteFile(path, []byte(item.role), item.mode); err != nil {
			t.Fatal(err)
		}
		input := Input{Name: item.name, Role: item.role, Mode: item.mode, SourcePath: path}
		if executableRole(item.role) {
			input.GoVersion = runtime.Version()
		}
		result = append(result, input)
	}
	return result
}
