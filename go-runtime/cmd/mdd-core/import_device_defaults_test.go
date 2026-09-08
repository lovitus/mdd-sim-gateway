package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systempreferences"
)

func TestDeviceDefaultsImportCommandPreservesChoicesAndReceipt(t *testing.T) {
	root := t.TempDir()
	source, database := filepath.Join(root, "desired.json"), filepath.Join(root, "preferences.db")
	payload := []byte(`{"version":2,"defaults":{"cellular_enabled":false,"vowifi_enabled":false,"roaming_enabled":true},"devices":{"existing":{"cellular_enabled":true}}}`)
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"--source", source, "--preferences", database}
	var first, second bytes.Buffer
	if err := runDeviceDefaultsImport(arguments, &first); err != nil {
		t.Fatal(err)
	}
	if err := runDeviceDefaultsImport(arguments, &second); err != nil {
		t.Fatal(err)
	}
	var receipt, repeated struct {
		Created  bool   `json:"created"`
		Revision uint64 `json:"revision"`
		Hash     string `json:"source_sha256"`
	}
	if err := json.Unmarshal(first.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Bytes(), &repeated); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if !receipt.Created || repeated.Created || receipt.Revision != repeated.Revision || receipt.Hash != hex.EncodeToString(digest[:]) {
		t.Fatal("invalid import receipt")
	}
	store, err := systempreferences.Open(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	store.Close()
	if err != nil || snapshot.Preferences.NewDeviceDefaults == nil || *snapshot.Preferences.NewDeviceDefaults != (systempreferences.NewDeviceDefaults{RoamingEnabled: true}) {
		t.Fatal("import lost explicit choices", err)
	}
	if err := os.WriteFile(source, []byte(`{"defaults":{"vowifi_enabled":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runDeviceDefaultsImport(arguments, &bytes.Buffer{}); err == nil {
		t.Fatal("different defaults overwrote existing intent")
	}
}

func TestDeviceDefaultsImportRejectsWrongSourceBeforeCreatingStore(t *testing.T) {
	root := t.TempDir()
	source, database := filepath.Join(root, "wrong.json"), filepath.Join(root, "absent.db")
	if err := os.WriteFile(source, []byte(`{"profiles":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runDeviceDefaultsImport([]string{"--source", source, "--preferences", database}, &bytes.Buffer{}); err == nil {
		t.Fatal("wrong document accepted")
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("failed parsing created database", err)
	}
}
