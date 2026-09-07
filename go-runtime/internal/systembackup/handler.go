// Package systembackup exposes a bounded, allowlisted backup of durable state.
// Credentials and mutable runtime sockets are intentionally never accepted as
// sources; callers must explicitly choose the safe state files.
package systembackup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/boltsnapshot"
)

const (
	maximumSourceBytes = boltsnapshot.MaximumBytes
	maximumBackupBytes = 128 << 20
)

type Source struct {
	Name, Path string
	Read       func() ([]byte, error)
}

type FileEvidence struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type Handler struct {
	sources []Source
	now     func() time.Time
}

type sourceFailure struct{ name, code string }

func (failure *sourceFailure) Error() string { return failure.code }

func NewHandler(sources []Source, now func() time.Time) (*Handler, error) {
	if len(sources) == 0 {
		return nil, errors.New("backup requires at least one source")
	}
	seen := map[string]struct{}{}
	copySources := make([]Source, len(sources))
	for index, source := range sources {
		source.Name, source.Path = strings.TrimSpace(source.Name), filepath.Clean(strings.TrimSpace(source.Path))
		if source.Name == "" || source.Name == "." || source.Name == ".." || source.Name == "manifest.json" || strings.ContainsAny(source.Name, "/\\\x00\r\n") || source.Read == nil && (!filepath.IsAbs(source.Path) || source.Path == string(filepath.Separator)) {
			return nil, errors.New("invalid backup source")
		}
		if _, exists := seen[source.Name]; exists {
			return nil, errors.New("duplicate backup source")
		}
		seen[source.Name] = struct{}{}
		copySources[index] = source
	}
	if now == nil {
		now = time.Now
	}
	return &Handler{sources: copySources, now: now}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	archive, err := handler.build()
	if err != nil {
		var failure *sourceFailure
		if errors.As(err, &failure) {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(response).Encode(map[string]any{"code": failure.code, "source": failure.name, "maximum_source_bytes": maximumSourceBytes, "maximum_total_bytes": maximumBackupBytes})
			return
		}
		writeError(response, http.StatusServiceUnavailable, "backup_unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set("Content-Disposition", `attachment; filename="mdd-state-backup.zip"`)
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(archive)
}

func (handler *Handler) build() ([]byte, error) {
	var output bytes.Buffer
	var totalBytes int64
	archive := zip.NewWriter(&output)
	manifest := struct {
		SchemaVersion int            `json:"schema_version"`
		CreatedAt     time.Time      `json:"created_at"`
		Files         []string       `json:"files"`
		Entries       []FileEvidence `json:"entries"`
		Consistency   string         `json:"consistency"`
	}{SchemaVersion: 1, CreatedAt: handler.now().UTC(), Consistency: "per_source_not_cross_database_atomic"}
	for _, source := range handler.sources {
		var err error
		var payload []byte
		if source.Read != nil {
			payload, err = source.Read()
			if err != nil {
				code := "backup_source_unavailable"
				if errors.Is(err, boltsnapshot.ErrTooLarge) {
					code = "backup_source_too_large"
				}
				return nil, &sourceFailure{name: source.Name, code: code}
			}
		} else {
			info, statErr := os.Lstat(source.Path)
			if statErr != nil {
				return nil, &sourceFailure{name: source.Name, code: "backup_source_unavailable"}
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return nil, &sourceFailure{name: source.Name, code: "backup_source_unavailable"}
			}
			if info.Size() > maximumSourceBytes {
				return nil, &sourceFailure{name: source.Name, code: "backup_source_too_large"}
			}
			input, openErr := os.Open(source.Path)
			if openErr != nil {
				return nil, &sourceFailure{name: source.Name, code: "backup_source_unavailable"}
			}
			payload, err = io.ReadAll(io.LimitReader(input, maximumSourceBytes+1))
			closeErr := input.Close()
			if err != nil {
				return nil, &sourceFailure{name: source.Name, code: "backup_source_unavailable"}
			}
			if closeErr != nil {
				return nil, &sourceFailure{name: source.Name, code: "backup_source_unavailable"}
			}
		}
		if int64(len(payload)) > maximumSourceBytes {
			return nil, &sourceFailure{name: source.Name, code: "backup_source_too_large"}
		}
		totalBytes += int64(len(payload))
		if totalBytes > maximumBackupBytes {
			return nil, &sourceFailure{name: source.Name, code: "backup_total_too_large"}
		}
		entry, err := archive.Create(filepath.ToSlash(source.Name))
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(payload); err != nil {
			return nil, err
		}
		manifest.Files = append(manifest.Files, source.Name)
		digest := sha256.Sum256(payload)
		manifest.Entries = append(manifest.Entries, FileEvidence{Name: source.Name, Bytes: len(payload), SHA256: hex.EncodeToString(digest[:])})
		if output.Len() > maximumBackupBytes {
			return nil, errors.New("backup exceeds maximum size")
		}
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	entry, err := archive.Create("manifest.json")
	if err != nil {
		return nil, err
	}
	if _, err := entry.Write(manifestBytes); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if output.Len() > maximumBackupBytes {
		return nil, errors.New("backup exceeds maximum size")
	}
	return output.Bytes(), nil
}

func writeError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"code": code})
}
