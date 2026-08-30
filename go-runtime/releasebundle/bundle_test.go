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
	inputs := fullTestInputs(t, root)
	output := filepath.Join(root, "release")
	manifest, err := CreateDirectory(output, Manifest{
		ReleaseID: "test-001", SourceRevision: strings.Repeat("a", 40), OS: "linux", Architecture: "amd64",
	}, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ReleaseID != "test-001" || len(manifest.Artifacts) != 8 {
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
	}, fullTestInputs(t, root)); err != nil {
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

func TestReleaseMayIncludeProviderApplyUnitWithoutBreakingOlderBundles(t *testing.T) {
	root := t.TempDir()
	inputs := testInputs(t, root)
	unit := filepath.Join(root, "input-mdd-provider-apply.service")
	if err := os.WriteFile(unit, []byte("provider apply unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, Input{Name: "mdd-provider-apply.service", Role: RoleApplyUnit, Mode: 0o644, SourcePath: unit})
	manifest, err := CreateDirectory(filepath.Join(root, "release-with-helper"), Manifest{
		ReleaseID: "test-helper", SourceRevision: strings.Repeat("c", 40), OS: "linux", Architecture: "amd64",
	}, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if artifact, found := manifest.Artifact(RoleApplyUnit); !found || artifact.Name != "mdd-provider-apply.service" {
		t.Fatalf("provider apply unit missing from manifest: %+v", manifest)
	}
}

func TestReleaseMayIncludeEgressUnitWithoutBreakingOlderBundles(t *testing.T) {
	root := t.TempDir()
	inputs := testInputs(t, root)
	unit := filepath.Join(root, "input-mdd-egress.service")
	if err := os.WriteFile(unit, []byte("egress unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, Input{Name: "mdd-egress.service", Role: RoleEgressUnit, Mode: 0o644, SourcePath: unit})
	manifest, err := CreateDirectory(filepath.Join(root, "release-with-egress"), Manifest{
		ReleaseID: "test-egress", SourceRevision: strings.Repeat("d", 40), OS: "linux", Architecture: "amd64",
	}, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if artifact, found := manifest.Artifact(RoleEgressUnit); !found || artifact.Name != "mdd-egress.service" {
		t.Fatalf("egress unit missing from manifest: %+v", manifest)
	}
}

func TestReleaseRejectsIncompleteLinuxAgentPair(t *testing.T) {
	for _, omitted := range []string{RoleAgent, RoleAgentUnit} {
		t.Run(omitted, func(t *testing.T) {
			root := t.TempDir()
			inputs := testInputs(t, root)
			for _, input := range agentInputs(t, root) {
				if input.Role != omitted {
					inputs = append(inputs, input)
				}
			}
			if _, err := CreateDirectory(filepath.Join(root, "release"), Manifest{
				ReleaseID: "incomplete-agent", SourceRevision: strings.Repeat("1", 40), OS: "linux", Architecture: "amd64",
			}, inputs); err == nil {
				t.Fatal("incomplete Linux Agent binary/unit pair was accepted")
			}
		})
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

func fullTestInputs(t *testing.T, root string) []Input {
	return append(testInputs(t, root), agentInputs(t, root)...)
}

func agentInputs(t *testing.T, root string) []Input {
	t.Helper()
	return materializeInputs(t, root, []struct {
		name, role string
		mode       os.FileMode
	}{
		{"mdd-agent", RoleAgent, 0o755},
		{"mdd-agent.service", RoleAgentUnit, 0o644},
	})
}

func materializeInputs(t *testing.T, root string, items []struct {
	name, role string
	mode       os.FileMode
}) []Input {
	t.Helper()
	result := make([]Input, 0, len(items))
	for _, item := range items {
		path := filepath.Join(root, "input-"+item.name)
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
