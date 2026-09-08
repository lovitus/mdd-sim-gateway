//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
)

func TestWebStartupWritePreservesOtherConfigAndBackup(t *testing.T) {
	dir := t.TempDir()
	cert, key, _, _, err := bootstrapTLSIdentity(rand.Reader, time.Now(), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, cert, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	settings := provideradmin.WebSettings{Listen: "127.0.0.1:8443", TLSCert: certPath, TLSKey: keyPath}
	document := map[string]any{"public": settings, "local": map[string]string{"token": "fixture-secret-preserved", "listen": "127.0.0.1:9081"}, "future_field": []string{"retained"}}
	payload, _ := json.Marshal(document)
	path := filepath.Join(dir, "core.json")
	if err := os.WriteFile(path, payload, 0640); err != nil {
		t.Fatal(err)
	}
	service := &providerApplyService{configPath: path}
	before, err := service.WebSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.Listen = "127.0.0.1:9443"
	input := provideradmin.WebRequest{SchemaVersion: 1, ExpectedRevision: before.Revision, Settings: settings}
	result, err := service.SaveWebSettings(context.Background(), input)
	if err != nil || result.Settings.Listen != settings.Listen || result.Revision == before.Revision {
		t.Fatal(result, err)
	}
	backup, err := os.ReadFile(path + ".before-web-change")
	if err != nil || !bytes.Equal(backup, payload) {
		t.Fatal("backup not exact", err)
	}
	changed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]json.RawMessage
	if json.Unmarshal(changed, &actual) != nil || !bytes.Contains(actual["local"], []byte("fixture-secret-preserved")) || !bytes.Contains(actual["future_field"], []byte("retained")) {
		t.Fatal("unrelated configuration lost")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("configuration permissions changed", err)
	}
	if _, err := service.SaveWebSettings(context.Background(), input); err == nil {
		t.Fatal("stale writer accepted")
	}
	input.ExpectedRevision = result.Revision
	input.Settings.TLSKey = filepath.Join(dir, "missing.key")
	if _, err := service.SaveWebSettings(context.Background(), input); err == nil {
		t.Fatal("missing TLS identity accepted")
	}
	final, _ := os.ReadFile(path)
	if !bytes.Equal(final, changed) {
		t.Fatal("failed write changed configuration")
	}
}
