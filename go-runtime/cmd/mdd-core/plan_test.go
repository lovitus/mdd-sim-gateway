package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

func TestProviderApplyPlanCommandIsReadOnly(t *testing.T) {
	directory := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != providerapply.Path || request.Header.Get("Authorization") != "Bearer "+localToken {
			http.Error(response, "rejected", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(response).Encode(providerapply.Snapshot{
			SchemaVersion: 1, CatalogRevision: 1,
			Lines: []providerapply.LineStatus{{LineID: "line-1", Code: "provider_absent"}},
		})
	}))
	defer server.Close()
	settings := config{}
	settings.Public.Listen = "127.0.0.1:8443"
	settings.Public.TLSCert = filepath.Join(directory, "tls.crt")
	settings.Public.TLSKey = filepath.Join(directory, "tls.key")
	settings.Local.Listen = strings.TrimPrefix(server.URL, "http://")
	settings.Local.Token = localToken
	settings.AuthPath = filepath.Join(directory, "auth.json")
	settings.EventsPath = filepath.Join(directory, "events.db")
	settings.MessagesPath = filepath.Join(directory, "messages.db")
	settings.CatalogPath = filepath.Join(directory, "lines.db")
	configPayload, _ := json.Marshal(settings)
	configPath := filepath.Join(directory, "core.json")
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	line := linecatalog.Line{
		ID: "line-1", Enabled: true, CardID: "8944100000000000001",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"},
	}
	candidate := filepath.Join(directory, "candidate")
	if _, err := renderProviderDirectory(settings, linecatalog.Snapshot{
		SchemaVersion: 1, Revision: 1, Lines: []linecatalog.Line{line},
	}, candidate, filepath.Join(directory, "state")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runProviderPlan([]string{"-config", configPath, "-candidate", candidate}, &output); err != nil {
		t.Fatal(err)
	}
	var plan providerapply.Plan
	if json.Unmarshal(output.Bytes(), &plan) != nil || !plan.Safe || len(plan.Added) != 1 || plan.Added[0].LineID != "line-1" {
		t.Fatalf("plan=%s", output.String())
	}
}
