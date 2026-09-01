package systemstatus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/buildidentity"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
)

type ProvenanceSource interface {
	CollectProvenance(context.Context) Provenance
}

type provenanceDependencies struct {
	executable func() (string, error)
	evalLinks  func(string) (string, error)
	lstat      func(string) (os.FileInfo, error)
	load       func(string) (releasebundle.Manifest, error)
	build      func() buildidentity.Identity
	goos       string
	goarch     string
}

type cachedProvenance struct {
	once   sync.Once
	deps   provenanceDependencies
	result Provenance
}

func newProvenanceSource() ProvenanceSource {
	return &cachedProvenance{deps: provenanceDependencies{
		executable: os.Executable,
		evalLinks:  filepath.EvalSymlinks,
		lstat:      os.Lstat,
		load:       releasebundle.LoadDirectory,
		build:      buildidentity.Read,
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
	}}
}

func (source *cachedProvenance) CollectProvenance(context.Context) Provenance {
	source.once.Do(func() { source.result = detectProvenance(source.deps) })
	return cloneProvenance(source.result)
}

func detectProvenance(deps provenanceDependencies) Provenance {
	executable, err := deps.executable()
	if err != nil || !filepath.IsAbs(executable) {
		return Provenance{Kind: "unknown", State: "unavailable", Code: "executable_unavailable"}
	}
	resolved, err := deps.evalLinks(executable)
	if err != nil || !filepath.IsAbs(resolved) {
		return Provenance{Kind: "unknown", State: "unavailable", Code: "executable_resolution_failed"}
	}
	info, err := deps.lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Provenance{Kind: "unknown", State: "unavailable", Code: "executable_type_invalid"}
	}
	directory := filepath.Dir(resolved)
	manifestPath := filepath.Join(directory, "manifest.json")
	manifestInfo, manifestErr := deps.lstat(manifestPath)
	if errors.Is(manifestErr, os.ErrNotExist) {
		return developmentProvenance(deps.build())
	}
	if manifestErr != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 {
		return Provenance{Kind: "release", State: "release_invalid", Code: "release_manifest_type_invalid"}
	}
	manifest, err := deps.load(directory)
	if err != nil {
		return Provenance{Kind: "release", State: "release_invalid", Code: "release_validation_failed"}
	}
	if manifest.OS != deps.goos || manifest.Architecture != deps.goarch {
		return Provenance{Kind: "release", State: "release_invalid", Code: "release_platform_mismatch"}
	}
	core, found := manifest.Artifact(releasebundle.RoleCore)
	expected := filepath.Join(directory, core.Name)
	if !found || core.Name != filepath.Base(resolved) || expected != resolved {
		return Provenance{Kind: "release", State: "release_invalid", Code: "release_core_mismatch"}
	}
	return Provenance{
		Kind: "release", State: "verified", Verified: true, Code: "release_manifest_verified",
		ReleaseID: manifest.ReleaseID, SourceRevision: manifest.SourceRevision, CoreSHA256: core.SHA256,
	}
}

func developmentProvenance(identity buildidentity.Identity) Provenance {
	return Provenance{
		Kind: "development", State: "development", Verified: false, Code: "development_build",
		ModuleVersion: identity.ModuleVersion, VCSRevision: identity.VCSRevision,
		VCSTime: identity.VCSTime, VCSModified: identity.VCSModified,
	}
}
