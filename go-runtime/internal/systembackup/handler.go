// Package systembackup exposes a bounded, allowlisted backup of durable state.
// Credentials and mutable runtime sockets are intentionally never accepted as
// sources; callers must explicitly choose the safe state files.
package systembackup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maximumSourceBytes = 32 << 20
	maximumBackupBytes = 128 << 20
)

type Source struct {
	Name, Path string
	Read       func() ([]byte, error)
}
type Handler struct {
	sources []Source
	now     func() time.Time
}

func NewHandler(sources []Source, now func() time.Time) (*Handler, error) {
	if len(sources) == 0 {
		return nil, errors.New("backup requires at least one source")
	}
	seen := map[string]struct{}{}
	copySources := make([]Source, len(sources))
	for index, source := range sources {
		source.Name, source.Path = strings.TrimSpace(source.Name), filepath.Clean(strings.TrimSpace(source.Path))
		if source.Name == "" || strings.ContainsAny(source.Name, "/\\\x00\r\n") || source.Read == nil && (!filepath.IsAbs(source.Path) || source.Path == string(filepath.Separator)) {
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
	archive := zip.NewWriter(&output)
	manifest := struct {
		SchemaVersion int       `json:"schema_version"`
		CreatedAt     time.Time `json:"created_at"`
		Files         []string  `json:"files"`
	}{SchemaVersion: 1, CreatedAt: handler.now().UTC()}
	for _, source := range handler.sources {
		var err error
		var payload []byte
		if source.Read != nil {
			payload, err = source.Read()
			if err != nil {
				return nil, err
			}
		} else {
			info, statErr := os.Lstat(source.Path)
			if statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) {
					continue
				}
				return nil, statErr
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maximumSourceBytes {
				return nil, errors.New("backup source is not a bounded regular file")
			}
			input, openErr := os.Open(source.Path)
			if openErr != nil {
				return nil, openErr
			}
			payload, err = io.ReadAll(io.LimitReader(input, maximumSourceBytes+1))
			closeErr := input.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
		if int64(len(payload)) > maximumSourceBytes {
			return nil, errors.New("backup source exceeds maximum size")
		}
		entry, err := archive.Create(filepath.ToSlash(source.Name))
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(payload); err != nil {
			return nil, err
		}
		manifest.Files = append(manifest.Files, source.Name)
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
