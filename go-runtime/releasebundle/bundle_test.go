package releasebundle

import (
	"encoding/json"
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
	if manifest.ReleaseID != "test-001" || len(manifest.Artifacts) != 16 {
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
	inputs := fullTestInputs(t, root)
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
	inputs := fullTestInputs(t, root)
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

func TestReleaseRejectsIncompleteLinuxAgentCapability(t *testing.T) {
	for _, omitted := range []string{RoleAgent, RoleAgentAudio, RoleAgentUnit} {
		t.Run(omitted, func(t *testing.T) {
			root := t.TempDir()
			var inputs []Input
			for _, input := range fullTestInputs(t, root) {
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

func TestReleaseRejectsMissingLegalArtifact(t *testing.T) {
	for _, omitted := range []string{RoleProjectLicense, RoleProjectNotice, RoleThirdParty, RoleGoLicenses} {
		t.Run(omitted, func(t *testing.T) {
			root := t.TempDir()
			var inputs []Input
			for _, input := range fullTestInputs(t, root) {
				if input.Role != omitted {
					inputs = append(inputs, input)
				}
			}
			if _, err := CreateDirectory(filepath.Join(root, "release"), Manifest{
				ReleaseID: "missing-legal", SourceRevision: strings.Repeat("2", 40), OS: "linux", Architecture: "amd64",
			}, inputs); err == nil {
				t.Fatal("release without required legal artifact was accepted")
			}
		})
	}
}

func TestCurrentReleaseRejectsMissingRuntimeArtifact(t *testing.T) {
	for _, omitted := range []string{RoleAgent, RoleAgentAudio, RoleAgentUnit, RoleGuardUnit, RoleApplyUnit, RoleEgressUnit} {
		t.Run(omitted, func(t *testing.T) {
			root := t.TempDir()
			var inputs []Input
			for _, input := range fullTestInputs(t, root) {
				if input.Role != omitted {
					inputs = append(inputs, input)
				}
			}
			if _, err := CreateDirectory(filepath.Join(root, "release"), Manifest{
				ReleaseID: "missing-runtime", SourceRevision: strings.Repeat("8", 40), OS: "linux", Architecture: "amd64",
			}, inputs); err == nil {
				t.Fatal("current release without required runtime artifact was accepted")
			}
		})
	}
}

func TestLegacySchemaOneManifestRemainsReadable(t *testing.T) {
	root := t.TempDir()
	inputs := append(testInputs(t, root)[:6], materializeInputs(t, root, []struct {
		name, role string
		mode       os.FileMode
	}{{"mdd-agent", RoleAgent, 0o755}, {"mdd-agent.service", RoleAgentUnit, 0o644}})...)
	manifest, err := CreateDirectory(filepath.Join(root, "release"), Manifest{
		ReleaseID: "legacy-001", SourceRevision: strings.Repeat("9", 40), OS: "linux", Architecture: "amd64",
	}, inputs)
	if err == nil || manifest.SchemaVersion != 0 {
		t.Fatal("current schema creation accepted a legacy incomplete artifact set")
	}
	legacy := Manifest{SchemaVersion: legacySchemaVersion, ReleaseID: "legacy-001", SourceRevision: strings.Repeat("9", 40), OS: "linux", Architecture: "amd64"}
	for _, input := range inputs {
		legacy.Artifacts = append(legacy.Artifacts, Artifact{
			Name: input.Name, Role: input.Role, Mode: modeString(input.Mode), Size: 1,
			SHA256: strings.Repeat("a", 64), GoVersion: input.GoVersion,
		})
	}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy schema one manifest rejected: %v", err)
	}
}

func TestPreviousSchemaTwoManifestWithoutGuardRemainsReadable(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "release")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: previousSchemaVersion, ReleaseID: "previous-002",
		SourceRevision: strings.Repeat("7", 40), OS: "linux", Architecture: "amd64"}
	for _, input := range fullTestInputs(t, root) {
		if input.Role == RoleGuardUnit {
			continue
		}
		payload, err := os.ReadFile(input.SourcePath)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, input.Name)
		if err := os.WriteFile(path, payload, input.Mode); err != nil {
			t.Fatal(err)
		}
		digest, err := fileDigest(path)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{Name: input.Name, Role: input.Role,
			Mode: modeString(input.Mode), Size: int64(len(payload)), SHA256: digest, GoVersion: input.GoVersion})
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), append(payload, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDirectory(directory)
	if err != nil || loaded.SchemaVersion != previousSchemaVersion {
		t.Fatalf("schema two release rejected: schema=%d err=%v", loaded.SchemaVersion, err)
	}
	if _, found := loaded.Artifact(RoleGuardUnit); found {
		t.Fatal("schema two release unexpectedly contained the new guard role")
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
		{"LICENSE", RoleProjectLicense, 0o644}, {"NOTICE", RoleProjectNotice, 0o644},
		{"THIRD_PARTY_LICENSES.md", RoleThirdParty, 0o644}, {"go-dependency-licenses.tar.gz", RoleGoLicenses, 0o644},
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
	inputs := append(testInputs(t, root), agentInputs(t, root)...)
	return append(inputs, materializeInputs(t, root, []struct {
		name, role string
		mode       os.FileMode
	}{
		{"mdd-cellular-guard.service", RoleGuardUnit, 0o644},
		{"mdd-provider-apply.service", RoleApplyUnit, 0o644},
		{"mdd-egress.service", RoleEgressUnit, 0o644},
	})...)
}

func agentInputs(t *testing.T, root string) []Input {
	t.Helper()
	return materializeInputs(t, root, []struct {
		name, role string
		mode       os.FileMode
	}{
		{"mdd-agent", RoleAgent, 0o755},
		{"mdd-call-audio-helper", RoleAgentAudio, 0o755},
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
