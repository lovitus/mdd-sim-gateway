package egressconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func productionConfig() Config {
	return Config{
		SchemaVersion: 2, Enabled: true, MissingPolicy: "error", RefreshMinutes: 30,
		Profiles: map[string]Profile{
			"node-gb": {Name: "London", Type: "node", Value: "ss://secret-node"},
			"sim-hk":  {Name: "Hong Kong SIM", Type: "cellular_sim", SIMICCID: "8985200000000000001"},
		},
		Exits: map[string]Exit{
			"gb": {Enabled: true, ProfileID: "node-gb", PinMode: "auto"},
			"hk": {Enabled: true, ProfileID: "sim-hk", Keywords: []string{"HK", "HK"}},
		},
	}
}

func TestLegacyProductionShapeImportsAndNoOpSaveKeepsRevision(t *testing.T) {
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "desired.json")
	legacy := map[string]any{"version": 1, "proxy": productionConfig(), "hardware": map[string]any{"auto_detect": true}}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	config, receipt, err := ReadLegacy(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Exits["gb"].PinMode; got != "" {
		t.Fatalf("legacy unpinned pin mode = %q, want empty", got)
	}

	store, err := Open(filepath.Join(directory, "egress.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ImportEmpty(config, receipt); err != nil {
		t.Fatal(err)
	}
	first, err := store.Snapshot()
	if err != nil || first.Revision != 2 {
		t.Fatalf("first snapshot=%+v err=%v", first, err)
	}
	unchanged, err := store.PutExpected(first.Config, first.Revision)
	if err != nil || unchanged.Revision != first.Revision {
		t.Fatalf("no-op save=%+v err=%v", unchanged, err)
	}
	changedConfig := cloneConfig(first.Config)
	changedConfig.Exits["gb"] = Exit{Enabled: false, ProfileID: "node-gb"}
	changed, err := store.PutExpected(changedConfig, first.Revision)
	if err != nil || changed.Revision != first.Revision+1 {
		t.Fatalf("changed save=%+v err=%v", changed, err)
	}
	staleConfig := cloneConfig(changed.Config)
	staleConfig.Enabled = false
	if _, err := store.PutExpected(staleConfig, first.Revision); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale save err=%v, want ErrRevision", err)
	}
	final, err := store.Snapshot()
	if err != nil || !final.Config.Enabled || final.Revision != changed.Revision {
		t.Fatalf("stale save changed store: snapshot=%+v err=%v", final, err)
	}
}

func TestConfigurationValidationRejectsBrokenReferencesAndIdentities(t *testing.T) {
	tests := map[string]func(*Config){
		"unknown profile": func(config *Config) {
			exit := config.Exits["gb"]
			exit.ProfileID = "missing"
			config.Exits["gb"] = exit
		},
		"invalid country": func(config *Config) {
			config.Exits["great-britain"] = config.Exits["gb"]
			delete(config.Exits, "gb")
		},
		"invalid ICCID": func(config *Config) {
			profile := config.Profiles["sim-hk"]
			profile.SIMICCID = "1234"
			config.Profiles["sim-hk"] = profile
		},
		"invalid pinned mode": func(config *Config) {
			exit := config.Exits["gb"]
			exit.PinnedNode, exit.PinMode = "London A", "auto"
			config.Exits["gb"] = exit
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := cloneConfig(productionConfig())
			mutate(&config)
			if err := config.normalizeAndValidate(); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestHTTPUsesStrongRevisionAndNeverOverwritesNewerConfiguration(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(filepath.Join(directory, "egress.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ImportEmpty(productionConfig(), ImportReceipt{SourceSHA256: strings.Repeat("a", 64), ImportedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store)
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/egress/config", nil))
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"2"` {
		t.Fatalf("GET=%d etag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}

	config := productionConfig()
	config.Enabled = false
	body, _ := json.Marshal(config)
	put := httptest.NewRequest(http.MethodPut, "/v1/egress/config", bytes.NewReader(body))
	put.Header.Set("If-Match", `"2"`)
	putResponse := httptest.NewRecorder()
	handler.ServeHTTP(putResponse, put)
	if putResponse.Code != http.StatusOK || putResponse.Header().Get("ETag") != `"3"` {
		t.Fatalf("PUT=%d etag=%q body=%s", putResponse.Code, putResponse.Header().Get("ETag"), putResponse.Body.String())
	}

	stale := httptest.NewRequest(http.MethodPut, "/v1/egress/config", bytes.NewReader(body))
	stale.Header.Set("If-Match", `"2"`)
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusPreconditionFailed || staleResponse.Header().Get("ETag") != `"3"` ||
		!strings.Contains(staleResponse.Body.String(), "egress_config_revision_changed") {
		t.Fatalf("stale PUT=%d etag=%q body=%s", staleResponse.Code, staleResponse.Header().Get("ETag"), staleResponse.Body.String())
	}
}
