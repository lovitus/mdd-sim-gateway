package systemstatus

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/buildidentity"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
)

type fakeFileInfo struct {
	name string
	mode os.FileMode
}

func (info fakeFileInfo) Name() string       { return info.name }
func (info fakeFileInfo) Size() int64        { return 1 }
func (info fakeFileInfo) Mode() os.FileMode  { return info.mode }
func (info fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (info fakeFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info fakeFileInfo) Sys() any           { return nil }

func TestDetectProvenanceVerifiesExactCoreArtifact(t *testing.T) {
	deps := verifiedProvenanceDependencies()
	result := detectProvenance(deps)
	if !result.Verified || result.Kind != "release" || result.State != "verified" ||
		result.ReleaseID != "mdd-release" || result.SourceRevision != testRevision || result.CoreSHA256 != testSHA {
		t.Fatalf("result=%+v", result)
	}
	deps.evalLinks = func(string) (string, error) { return "/release/other-core", nil }
	if result := detectProvenance(deps); result.State != "release_invalid" || result.Code != "release_core_mismatch" || result.Verified {
		t.Fatalf("mismatch=%+v", result)
	}
}

func TestDetectProvenanceOnlyFallsBackWhenManifestIsAbsent(t *testing.T) {
	deps := verifiedProvenanceDependencies()
	buildCalled := 0
	deps.build = func() buildidentity.Identity {
		buildCalled++
		modified := true
		return buildidentity.Identity{VCSRevision: testRevision, VCSModified: &modified}
	}
	deps.lstat = func(path string) (os.FileInfo, error) {
		if filepath.Base(path) == "manifest.json" {
			return nil, os.ErrNotExist
		}
		return fakeFileInfo{name: filepath.Base(path), mode: 0o755}, nil
	}
	result := detectProvenance(deps)
	if result.Kind != "development" || result.State != "development" || result.Verified ||
		result.VCSRevision != testRevision || buildCalled != 1 {
		t.Fatalf("development=%+v buildCalled=%d", result, buildCalled)
	}
	deps.lstat = func(path string) (os.FileInfo, error) {
		if filepath.Base(path) == "manifest.json" {
			return nil, fs.ErrPermission
		}
		return fakeFileInfo{name: filepath.Base(path), mode: 0o755}, nil
	}
	buildCalled = 0
	result = detectProvenance(deps)
	if result.Kind != "release" || result.State != "release_invalid" || result.Verified || buildCalled != 0 {
		t.Fatalf("permission=%+v buildCalled=%d", result, buildCalled)
	}
}

func TestDetectProvenanceDoesNotHideManifestValidationFailure(t *testing.T) {
	deps := verifiedProvenanceDependencies()
	deps.load = func(string) (releasebundle.Manifest, error) { return releasebundle.Manifest{}, errors.New("corrupt") }
	result := detectProvenance(deps)
	if result.Kind != "release" || result.State != "release_invalid" ||
		result.Code != "release_validation_failed" || result.Verified {
		t.Fatalf("result=%+v", result)
	}
}

func TestCachedProvenanceRunsExpensiveDetectionOnce(t *testing.T) {
	deps := verifiedProvenanceDependencies()
	loads := 0
	base := deps.load
	deps.load = func(path string) (releasebundle.Manifest, error) {
		loads++
		return base(path)
	}
	source := &cachedProvenance{deps: deps}
	for range 3 {
		if result := source.CollectProvenance(t.Context()); !result.Verified {
			t.Fatalf("result=%+v", result)
		}
	}
	if loads != 1 {
		t.Fatalf("loads=%d", loads)
	}
}

const (
	testRevision = "bc378f003960645e89ac133f84fcf583a5dfb1f7"
	testSHA      = "daf950ea38053813e70020f924570002d0150910cec2fe6c07f0132b83604a2b"
)

func verifiedProvenanceDependencies() provenanceDependencies {
	return provenanceDependencies{
		executable: func() (string, error) { return "/release/mdd-core", nil },
		evalLinks:  func(path string) (string, error) { return path, nil },
		lstat: func(path string) (os.FileInfo, error) {
			mode := os.FileMode(0o755)
			if filepath.Base(path) == "manifest.json" {
				mode = 0o644
			}
			return fakeFileInfo{name: filepath.Base(path), mode: mode}, nil
		},
		load: func(string) (releasebundle.Manifest, error) {
			return releasebundle.Manifest{
				SchemaVersion: releasebundle.SchemaVersion, ReleaseID: "mdd-release",
				SourceRevision: testRevision, OS: "linux", Architecture: "amd64",
				Artifacts: []releasebundle.Artifact{{Name: "mdd-core", Role: releasebundle.RoleCore, SHA256: testSHA}},
			}, nil
		},
		build: buildidentity.Read, goos: "linux", goarch: "amd64",
	}
}
