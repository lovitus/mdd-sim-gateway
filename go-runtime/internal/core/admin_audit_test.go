package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systempreferences"
)

func TestAdministrativeAuditPreservesResponseAndExcludesSecrets(t *testing.T) {
	root := t.TempDir()
	store, err := events.OpenBoltStore(filepath.Join(root, "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	preferences, err := systempreferences.Open(filepath.Join(root, "preferences.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer preferences.Close()
	replay, err := events.NewReplay(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(replay, nil, WithAdminAudit(preferences, store))
	server.mux.HandleFunc("POST /v1/test/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202); _, _ = w.Write([]byte("accepted")) })
	request := httptest.NewRequest("POST", "/v1/test/private-path-value?token=private-query-value", strings.NewReader(`{"password":"private-body-value"}`))
	request.Header.Set("Authorization", "Bearer private-header-value")
	request.Header.Set("X-Forwarded-For", "203.0.113.5")
	request.RemoteAddr = "192.0.2.10:9000"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != 202 || response.Body.String() != "accepted" {
		t.Fatal(response.Code, response.Body.String())
	}
	entries, err := store.AdminAudit(200)
	if err != nil || len(entries) != 1 || entries[0].Route != "/v1/test/{id}" || entries[0].Client != "192.0.2.10" {
		t.Fatal(entries, err)
	}
	wire, _ := json.Marshal(entries)
	if strings.Contains(string(wire), "private-") {
		t.Fatal("audit recorded a secret")
	}
	current, err := preferences.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	current.Preferences.AuditEnabled = &disabled
	if _, err := preferences.PutExpected(current.Preferences, current.Revision); err != nil {
		t.Fatal(err)
	}
	server.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/test/other", nil))
	entries, err = store.AdminAudit(200)
	if err != nil || len(entries) != 1 {
		t.Fatal("disabled audit kept writing", entries, err)
	}
}

func TestAuditForwardingTrustDoesNotAcceptUntrustedHeaders(t *testing.T) {
	request := httptest.NewRequest("POST", "/", nil)
	request.RemoteAddr = "192.0.2.10:9000"
	request.Header.Set("X-Forwarded-For", "203.0.113.5, 192.0.2.20")
	if got := auditClient(request, nil); got != "192.0.2.10" {
		t.Fatal(got)
	}
	if got := auditClient(request, []string{"192.0.2.0/24"}); got != "203.0.113.5" {
		t.Fatal(got)
	}
	request.Header.Set("X-Forwarded-For", "not-an-address")
	if got := auditClient(request, []string{"192.0.2.0/24"}); got != "192.0.2.10" {
		t.Fatal(got)
	}
	request.RemoteAddr = "[2001:db8::10]:443"
	request.Header.Set("X-Forwarded-For", "2001:db8:1::5")
	if got := auditClient(request, []string{"2001:db8::/64"}); got != "2001:db8:1::5" {
		t.Fatal(got)
	}
}
