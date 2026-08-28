package providerconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maximumManifestBytes = 1 << 20

type Manifest struct {
	SchemaVersion   int             `json:"schema_version"`
	CatalogRevision uint64          `json:"catalog_revision"`
	Providers       []ManifestEntry `json:"providers"`
}

type ManifestEntry struct {
	LineID       string `json:"line_id"`
	UnitInstance string `json:"unit_instance"`
	ConfigFile   string `json:"config_file"`
	ConfigSHA256 string `json:"config_sha256"`
}

func LoadDirectory(path string) (Manifest, error) {
	var manifest Manifest
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return manifest, errors.New("provider directory must be absolute and scoped")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return manifest, errors.New("provider directory must be a real directory")
	}
	payload, err := readBounded(filepath.Join(path, "manifest.json"), maximumManifestBytes)
	if err != nil {
		return manifest, err
	}
	if err := decodeStrict(payload, &manifest); err != nil {
		return manifest, err
	}
	if manifest.SchemaVersion != 1 || manifest.CatalogRevision == 0 {
		return Manifest{}, errors.New("invalid provider manifest schema or revision")
	}
	lines, instances, files := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, entry := range manifest.Providers {
		if strings.TrimSpace(entry.LineID) == "" || strings.TrimSpace(entry.UnitInstance) == "" ||
			entry.UnitInstance != UnitInstance(entry.LineID) ||
			entry.ConfigFile != entry.UnitInstance+".json" || filepath.Base(entry.ConfigFile) != entry.ConfigFile ||
			len(entry.ConfigSHA256) != 64 {
			return Manifest{}, errors.New("invalid provider manifest entry")
		}
		if _, exists := lines[entry.LineID]; exists {
			return Manifest{}, errors.New("duplicate provider manifest line")
		}
		if _, exists := instances[entry.UnitInstance]; exists {
			return Manifest{}, errors.New("duplicate provider unit instance")
		}
		if _, exists := files[entry.ConfigFile]; exists {
			return Manifest{}, errors.New("duplicate provider config file")
		}
		configPayload, err := readBounded(filepath.Join(path, entry.ConfigFile), 64<<10)
		if err != nil {
			return Manifest{}, err
		}
		digest := sha256.Sum256(configPayload)
		if !strings.EqualFold(entry.ConfigSHA256, hex.EncodeToString(digest[:])) {
			return Manifest{}, errors.New("provider config hash does not match manifest")
		}
		var config Config
		if err := decodeStrict(configPayload, &config); err != nil || config.Validate() != nil || config.LineID != entry.LineID {
			return Manifest{}, errors.New("provider config does not match manifest")
		}
		lines[entry.LineID], instances[entry.UnitInstance], files[entry.ConfigFile] = struct{}{}, struct{}{}, struct{}{}
	}
	return manifest, nil
}

func UnitInstance(lineID string) string {
	digest := sha256.Sum256([]byte(lineID))
	return "line-" + hex.EncodeToString(digest[:16])
}

func readBounded(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximum {
		return nil, errors.New("provider artifact is not a bounded regular file")
	}
	return os.ReadFile(path)
}

func decodeStrict(payload []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("provider artifact has trailing JSON")
	}
	return nil
}
