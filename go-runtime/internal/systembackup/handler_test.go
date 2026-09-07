package systembackup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/boltsnapshot"
)

func TestBackupReportsSourceAndEnforcesUncompressedTotal(t *testing.T) {
	for _, code := range []string{"backup_source_too_large", "backup_total_too_large"} {
		var sources []Source
		if code == "backup_source_too_large" {
			sources = []Source{{Name: "events.db", Read: func() ([]byte, error) { return nil, boltsnapshot.ErrTooLarge }}}
		} else {
			value := make([]byte, 65<<20)
			read := func() ([]byte, error) { return value, nil }
			sources = []Source{{Name: "first.db", Read: read}, {Name: "events.db", Read: read}}
		}
		handler, err := NewHandler(sources, nil)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/system/backups", nil))
		var result struct {
			Code   string `json:"code"`
			Source string `json:"source"`
		}
		if response.Code != 503 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Code != code || result.Source != "events.db" {
			t.Fatalf("response=%d %s", response.Code, response.Body.String())
		}
	}
}

func TestBackupFailureNeverReturnsPartialZIP(t *testing.T) {
	for _, source := range []Source{
		{Name: "missing.db", Path: filepath.Join(t.TempDir(), "missing.db")},
		{Name: "failed.db", Read: func() ([]byte, error) { return nil, errors.New("snapshot failed") }},
		{Name: "large.db", Read: func() ([]byte, error) { return make([]byte, maximumSourceBytes+1), nil }},
	} {
		handler, err := NewHandler([]Source{{Name: "good.json", Read: func() ([]byte, error) { return []byte("{}"), nil }}, source}, nil)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/system/backups", nil))
		if response.Code != 503 || response.Header().Get("Content-Type") == "application/zip" || bytes.HasPrefix(response.Body.Bytes(), []byte("PK")) {
			t.Fatal("partial backup returned")
		}
	}
	for _, name := range []string{"manifest.json", ".", "..", "../state.db"} {
		if _, err := NewHandler([]Source{{Name: name, Read: func() ([]byte, error) { return nil, nil }}}, nil); err == nil {
			t.Fatalf("invalid entry %q accepted", name)
		}
	}
}

func TestBackupIsBoundedAllowlistedAndContainsManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	if err := os.WriteFile(path, []byte("durable-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler([]Source{{Name: "catalog.db", Path: path}}, func() time.Time { return time.Unix(1_800_000_000, 0) })
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/system/backups", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("status=%d headers=%v", response.Code, response.Header())
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range archive.File {
		seen[file.Name] = true
	}
	if !seen["catalog.db"] || !seen["manifest.json"] || seen["auth.json"] {
		t.Fatalf("entries=%v", seen)
	}
	manifestFile, _ := archive.File[1].Open()
	defer manifestFile.Close()
	var manifest struct {
		Files       []string       `json:"files"`
		Entries     []FileEvidence `json:"entries"`
		Consistency string         `json:"consistency"`
	}
	if err := json.NewDecoder(manifestFile).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0] != "catalog.db" {
		t.Fatalf("manifest=%+v", manifest)
	}
	digest := sha256.Sum256([]byte("durable-state"))
	if len(manifest.Entries) != 1 || manifest.Entries[0].SHA256 != hex.EncodeToString(digest[:]) || manifest.Entries[0].Bytes != len("durable-state") || manifest.Consistency != "per_source_not_cross_database_atomic" {
		t.Fatalf("backup evidence=%+v", manifest)
	}
}

func TestBackupRejectsSymlinkSource(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	link := filepath.Join(directory, "link")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler([]Source{{Name: "link", Path: link}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/system/backups", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestBackupSupportsSanitizedProjectionSource(t *testing.T) {
	handler, err := NewHandler([]Source{{Name: "catalog.json", Read: func() ([]byte, error) { return []byte(`{"password_configured":true}`), nil }}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/system/backups", nil))
	if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte(`"password":"`)) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
