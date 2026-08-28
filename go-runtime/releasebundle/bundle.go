// Package releasebundle defines the strict, versioned Linux release directory
// consumed by the Go installer. It contains no service lifecycle operations.
package releasebundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	SchemaVersion       = 1
	maximumManifestSize = 64 << 10
	maximumArtifactSize = 512 << 20
)

const (
	RoleCore           = "core"
	RoleAgent          = "agent"
	RoleProvider       = "provider"
	RoleCoreUnit       = "core_unit"
	RoleProviderUnit   = "provider_unit"
	RoleProviderSource = "provider_source"
	RoleProviderNotice = "provider_notice"
)

type Manifest struct {
	SchemaVersion  int        `json:"schema_version"`
	ReleaseID      string     `json:"release_id"`
	SourceRevision string     `json:"source_revision"`
	OS             string     `json:"os"`
	Architecture   string     `json:"architecture"`
	Artifacts      []Artifact `json:"artifacts"`
}

type Artifact struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	Mode      string `json:"mode"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	GoVersion string `json:"go_version,omitempty"`
}

type Input struct {
	Name       string
	Role       string
	Mode       os.FileMode
	SourcePath string
	GoVersion  string
}

var (
	releaseIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	hexPattern       = regexp.MustCompile(`^[0-9a-f]+$`)
)

func CreateDirectory(output string, manifest Manifest, inputs []Input) (Manifest, error) {
	output = filepath.Clean(strings.TrimSpace(output))
	if !scopedAbsolute(output) || len(inputs) == 0 {
		return Manifest{}, errors.New("release output and artifacts are required")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Manifest{}, errors.New("release output already exists")
		}
		return Manifest{}, err
	}
	manifest.SchemaVersion = SchemaVersion
	manifest.Artifacts = make([]Artifact, 0, len(inputs))
	seen := map[string]struct{}{}
	for _, input := range inputs {
		if err := validateInput(input, seen); err != nil {
			return Manifest{}, err
		}
		info, err := os.Lstat(input.SourcePath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > maximumArtifactSize {
			return Manifest{}, errors.New("release input must be a bounded regular file")
		}
		digest, err := fileDigest(input.SourcePath)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{
			Name: input.Name, Role: input.Role, Mode: modeString(input.Mode), Size: info.Size(), SHA256: digest, GoVersion: input.GoVersion,
		})
	}
	sort.Slice(manifest.Artifacts, func(left, right int) bool { return manifest.Artifacts[left].Name < manifest.Artifacts[right].Name })
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Manifest{}, err
	}
	staging, err := os.MkdirTemp(parent, ".mdd-release-*")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		_ = os.RemoveAll(staging)
		return Manifest{}, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(staging)
		}
	}()
	byName := make(map[string]Input, len(inputs))
	for _, input := range inputs {
		byName[input.Name] = input
	}
	for _, artifact := range manifest.Artifacts {
		input := byName[artifact.Name]
		if err := copyFile(input.SourcePath, filepath.Join(staging, artifact.Name), input.Mode); err != nil {
			return Manifest{}, err
		}
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := writeFile(filepath.Join(staging, "manifest.json"), append(payload, '\n'), 0o644); err != nil {
		return Manifest{}, err
	}
	if err := syncDirectory(staging); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(staging, output); err != nil {
		return Manifest{}, err
	}
	complete = true
	if err := syncDirectory(parent); err != nil {
		return Manifest{}, err
	}
	return LoadDirectory(output)
}

func LoadDirectory(directory string) (Manifest, error) {
	var manifest Manifest
	directory = filepath.Clean(strings.TrimSpace(directory))
	if !scopedAbsolute(directory) {
		return manifest, errors.New("release directory must be absolute and scoped")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return manifest, errors.New("release directory must be a real directory")
	}
	payload, err := readBounded(filepath.Join(directory, "manifest.json"), maximumManifestSize)
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("release manifest has trailing JSON")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	expected := map[string]Artifact{"manifest.json": {Name: "manifest.json"}}
	for _, artifact := range manifest.Artifacts {
		expected[artifact.Name] = artifact
		path := filepath.Join(directory, artifact.Name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != parseMode(artifact.Mode) || info.Size() != artifact.Size {
			return Manifest{}, errors.New("release artifact type, mode, or size does not match manifest")
		}
		digest, err := fileDigest(path)
		if err != nil || digest != artifact.SHA256 {
			return Manifest{}, errors.New("release artifact digest does not match manifest")
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return Manifest{}, err
	}
	if len(entries) != len(expected) {
		return Manifest{}, errors.New("release directory contains unexpected files")
	}
	for _, entry := range entries {
		if _, found := expected[entry.Name()]; !found {
			return Manifest{}, errors.New("release directory contains an unexpected file")
		}
	}
	return manifest, nil
}

func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != SchemaVersion || !releaseIDPattern.MatchString(manifest.ReleaseID) ||
		!validRevision(manifest.SourceRevision) || manifest.OS != "linux" ||
		(manifest.Architecture != "amd64" && manifest.Architecture != "arm64") ||
		len(manifest.Artifacts) < 6 {
		return errors.New("invalid release manifest identity")
	}
	seenNames, seenRoles := map[string]struct{}{}, map[string]struct{}{}
	for _, artifact := range manifest.Artifacts {
		if filepath.Base(artifact.Name) != artifact.Name || artifact.Name == "manifest.json" || artifact.Name == "." ||
			!validRole(artifact.Role) || (artifact.Mode != "0644" && artifact.Mode != "0755") || artifact.Size < 1 ||
			artifact.Size > maximumArtifactSize || len(artifact.SHA256) != 64 || !hexPattern.MatchString(artifact.SHA256) {
			return errors.New("invalid release artifact")
		}
		if _, found := seenNames[artifact.Name]; found {
			return errors.New("duplicate release artifact name")
		}
		if _, found := seenRoles[artifact.Role]; found {
			return errors.New("duplicate release artifact role")
		}
		if executableRole(artifact.Role) != (artifact.Mode == "0755") {
			return errors.New("release artifact mode does not match role")
		}
		if executableRole(artifact.Role) != strings.HasPrefix(artifact.GoVersion, "go1.") {
			return errors.New("release artifact Go version does not match role")
		}
		seenNames[artifact.Name], seenRoles[artifact.Role] = struct{}{}, struct{}{}
	}
	for _, role := range []string{RoleCore, RoleProvider, RoleCoreUnit, RoleProviderUnit, RoleProviderSource, RoleProviderNotice} {
		if _, found := seenRoles[role]; !found {
			return errors.New("release manifest is missing a required role")
		}
	}
	return nil
}

func (manifest Manifest) Artifact(role string) (Artifact, bool) {
	for _, artifact := range manifest.Artifacts {
		if artifact.Role == role {
			return artifact, true
		}
	}
	return Artifact{}, false
}

func validateInput(input Input, seen map[string]struct{}) error {
	if filepath.Base(input.Name) != input.Name || input.Name == "." || input.Name == "manifest.json" ||
		!validRole(input.Role) || (input.Mode != 0o644 && input.Mode != 0o755) || !filepath.IsAbs(input.SourcePath) ||
		executableRole(input.Role) != (input.Mode == 0o755) || executableRole(input.Role) != strings.HasPrefix(input.GoVersion, "go1.") {
		return errors.New("invalid release input")
	}
	if _, found := seen[input.Name]; found {
		return errors.New("duplicate release input name")
	}
	seen[input.Name] = struct{}{}
	return nil
}

func validRole(role string) bool {
	switch role {
	case RoleCore, RoleAgent, RoleProvider, RoleCoreUnit, RoleProviderUnit, RoleProviderSource, RoleProviderNotice:
		return true
	default:
		return false
	}
}

func executableRole(role string) bool {
	return role == RoleCore || role == RoleAgent || role == RoleProvider
}
func validRevision(value string) bool {
	return (len(value) == 40 || len(value) == 64) && hexPattern.MatchString(value)
}
func scopedAbsolute(path string) bool {
	return filepath.IsAbs(path) && path != string(filepath.Separator)
}
func modeString(mode os.FileMode) string {
	if mode == 0o755 {
		return "0755"
	}
	return "0644"
}
func parseMode(value string) os.FileMode {
	if value == "0755" {
		return 0o755
	}
	return 0o644
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, io.LimitReader(file, maximumArtifactSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return "", errors.Join(copyErr, closeErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		_ = input.Close()
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, maximumArtifactSize+1))
	return errors.Join(copyErr, input.Close(), output.Sync(), output.Close())
}

func writeFile(path string, payload []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(payload)
	return errors.Join(writeErr, file.Sync(), file.Close())
}

func readBounded(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > maximum {
		return nil, errors.New("release file is not a bounded regular file")
	}
	return os.ReadFile(path)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
