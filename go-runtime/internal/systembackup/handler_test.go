package systembackup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
		Files []string `json:"files"`
	}
	if err := json.NewDecoder(manifestFile).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0] != "catalog.db" {
		t.Fatalf("manifest=%+v", manifest)
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
