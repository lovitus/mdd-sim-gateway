package egressconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maximumLegacyDesiredBytes = 1 << 20

type legacyDesired struct {
	Version int    `json:"version"`
	Proxy   Config `json:"proxy"`
}

func ReadLegacy(path string) (Config, ImportReceipt, error) {
	var config Config
	var receipt ImportReceipt
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return config, receipt, errors.New("legacy country exit desired path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return config, receipt, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumLegacyDesiredBytes {
		return config, receipt, errors.New("legacy country exit desired state must be a non-empty regular file no larger than 1 MiB")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return config, receipt, errors.New("legacy country exit desired state contains secrets and must not be accessible by group or others")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return config, receipt, err
	}
	var desired legacyDesired
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&desired); err != nil {
		return config, receipt, fmt.Errorf("decode legacy country exit desired state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return config, receipt, errors.New("legacy country exit desired state must contain one JSON document")
	}
	if desired.Version != 1 {
		return config, receipt, errors.New("legacy country exit desired state has an unsupported schema")
	}
	if err := desired.Proxy.normalizeAndValidate(); err != nil {
		return config, receipt, fmt.Errorf("legacy country exit desired state: %w", err)
	}
	digest := sha256.Sum256(payload)
	return desired.Proxy, ImportReceipt{
		SourceSHA256: hex.EncodeToString(digest[:]), ImportedAt: time.Now().UTC(),
	}, nil
}
