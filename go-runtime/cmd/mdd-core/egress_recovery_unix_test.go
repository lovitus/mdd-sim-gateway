//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

func TestRecoveryPreflightAndResumeUseFullAuthenticatedPath(t *testing.T) {
	token := strings.Repeat("t", 32)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != providerapply.Path || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected preflight request path=%s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(providerapply.Snapshot{SchemaVersion: 1, CatalogRevision: 7, Lines: []providerapply.LineStatus{}})
	}))
	defer server.Close()
	service := &providerApplyService{}
	service.settings.Local.Token = token
	request := egressconfig.RecoveryRequest{CatalogRevision: 7, Country: "gb", FailureID: strings.Repeat("a", 64)}
	if err := service.validateRecoveryPeers(context.Background(), server.URL, linecatalog.Snapshot{Revision: 7}, "lease", request); err != nil {
		t.Fatal(err)
	}
	if err := service.resumeRecoveryLease(context.Background(), server.URL, request); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestRecoveryDocumentIsFencedAndReplayDoesNotReselect(t *testing.T) {
	root := t.TempDir()
	configSnapshot := egressconfig.Snapshot{SchemaVersion: egressconfig.SchemaVersion, Revision: 3, Config: egressconfig.Config{
		SchemaVersion: egressconfig.SchemaVersion, Enabled: true,
		Profiles: map[string]egressconfig.Profile{"feed": {Name: "fixture", Type: "subscription", URL: "https://example.invalid/feed"}},
		Exits:    map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "feed"}},
	}}
	catalog := linecatalog.Snapshot{SchemaVersion: 1, Revision: 4, Lines: []linecatalog.Line{{ID: "line-1", Enabled: true, Network: linecatalog.NetworkConfig{EgressCountry: "gb"}}}}
	document, err := egressdesired.Render(configSnapshot, catalog, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	service := &providerApplyService{}
	service.settings.ProviderApply.EgressDesiredPath = filepath.Join(root, "desired.json")
	service.settings.ProviderApply.EgressStatusPath = filepath.Join(root, "status.json")
	if _, err := egressdesired.Publish(service.settings.ProviderApply.EgressDesiredPath, document); err != nil {
		t.Fatal(err)
	}
	writeStatus := func(generation, node string) {
		t.Helper()
		payload, err := json.Marshal(egressstatus.Snapshot{DesiredGeneration: generation, Exits: map[string]egressstatus.Exit{
			"gb": {Ready: true, Node: node, Candidates: []string{"node-a", "node-b"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(service.settings.ProviderApply.EgressStatusPath, payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeStatus(document.Generation, "node-a")
	request := egressconfig.RecoveryRequest{SchemaVersion: egressconfig.SchemaVersion, ConfigRevision: 3, CatalogRevision: 4,
		LineID: "line-1", ProviderGeneration: "provider-1", FailureID: strings.Repeat("a", 64), ExpectedGeneration: document.Generation,
		Country: "gb", FromNode: "node-a", ToNode: "node-b"}
	selected, replayed, err := service.recoveryDocument(configSnapshot, catalog, document, request)
	if err != nil || replayed != nil || selected.Generation == document.Generation || selected.RecoverySelections["gb"] != request {
		t.Fatal(selected, replayed, err)
	}
	if configSnapshot.Config.Exits["gb"].PinnedNode != "" {
		t.Fatal("user desired state changed")
	}
	if _, err := egressdesired.Publish(service.settings.ProviderApply.EgressDesiredPath, selected); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.recoveryDocument(configSnapshot, catalog, document, request); err == nil {
		t.Fatal("unconfirmed runtime accepted")
	}
	writeStatus(selected.Generation, "node-b")
	_, replayed, err = service.recoveryDocument(configSnapshot, catalog, document, request)
	if err != nil || replayed == nil || replayed.State != "unchanged" || replayed.Generation != selected.Generation {
		t.Fatal(replayed, err)
	}
	request.ToNode = "node-c"
	if _, _, err := service.recoveryDocument(configSnapshot, catalog, document, request); err == nil {
		t.Fatal("failure identity reused for another target")
	}
}
