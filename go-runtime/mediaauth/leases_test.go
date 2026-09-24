package mediaauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
)

func leaseVerifier(subject string) SessionVerifier {
	return SessionVerifierFunc(func(context.Context, *http.Request) (string, error) { return subject, nil })
}

func TestRecoveryLeaseRequiresProviderReceiptSupportBeforeAdmission(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Error("unexpected provider operation", r.URL.Path)
		}
		w.WriteHeader(200)
	}))
	defer provider.Close()
	providers := NewProviderDirectory()
	if err := providers.Replace(Provider{LineID: "line-1", ProviderID: "provider-1", Generation: "generation-1", CardID: "8944100000000000001", BaseURL: "ws" + strings.TrimPrefix(provider.URL, "http"), Token: strings.Repeat("t", 32)}); err != nil {
		t.Fatal(err)
	}
	store, err := callhistory.Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	router, _ := NewRouter(leaseVerifier("admin-1"), providers, nil, 0)
	handler, err := NewLeaseHandler(router, providers, leaseTestAuth{subject: "admin-1"}, time.Minute, store)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/media/leases", strings.NewReader(`{"line_id":"line-1","call_id":"call-1","operation_id":"start-1","recovery_key":"`+strings.Repeat("a", 64)+`"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "provider_recovery_upgrade_required") || len(router.leases) != 0 {
		t.Fatalf("old provider admitted recovery lease: %d %s", w.Code, w.Body.String())
	}
}

type leaseTestAuth struct {
	subject string
	err     error
}

func (auth leaseTestAuth) AuthorizeBrowserMutation(*http.Request) (string, error) {
	return auth.subject, auth.err
}

func TestLeaseHandlerIssuesAndRevokesCurrentProviderCapability(t *testing.T) {
	providers := NewProviderDirectory()
	if err := providers.Replace(Provider{
		LineID: "line-1", ProviderID: "vowifi-1", Generation: "provider-1", BaseURL: "ws://127.0.0.1:9010",
		Token: "0123456789abcdef0123456789abcdef",
	}); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(leaseVerifier("admin-1"), providers, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLeaseHandler(router, providers, leaseTestAuth{subject: "admin-1"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	issue := httptest.NewRequest(http.MethodPost, "/v1/media/leases", strings.NewReader(`{"line_id":"line-1","call_id":"call-1"}`))
	issued := httptest.NewRecorder()
	handler.ServeHTTP(issued, issue)
	if issued.Code != http.StatusCreated || !strings.Contains(issued.Body.String(), `"ws_path":"/api/browser-media/`) {
		t.Fatalf("issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	start := strings.Index(issued.Body.String(), `"session_id":"`) + len(`"session_id":"`)
	end := strings.Index(issued.Body.String()[start:], `"`)
	if start < len(`"session_id":"`) || end < 1 {
		t.Fatalf("missing session ID in %s", issued.Body.String())
	}
	sessionID := issued.Body.String()[start : start+end]

	revoke := httptest.NewRequest(http.MethodDelete, "/v1/media/leases", strings.NewReader(`{"session_id":"`+sessionID+`"}`))
	revoked := httptest.NewRecorder()
	handler.ServeHTTP(revoked, revoke)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestLeaseHandlerRejectsMissingProviderAuthorizationAndUnknownFields(t *testing.T) {
	providers := NewProviderDirectory()
	router, _ := NewRouter(leaseVerifier("admin-1"), providers, nil, 0)
	handler, _ := NewLeaseHandler(router, providers, leaseTestAuth{subject: "admin-1"}, time.Minute)
	request := httptest.NewRequest(http.MethodPost, "/v1/media/leases", strings.NewReader(`{"line_id":"line-1","call_id":"call-1"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("missing provider status=%d body=%s", response.Code, response.Body.String())
	}

	denied, _ := NewLeaseHandler(router, providers, leaseTestAuth{err: errors.New("denied")}, time.Minute)
	request = httptest.NewRequest(http.MethodPost, "/v1/media/leases", strings.NewReader(`{"line_id":"line-1","call_id":"call-1","extra":true}`))
	response = httptest.NewRecorder()
	denied.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("authorization status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/media/leases", strings.NewReader(`{"line_id":"line-1","call_id":"call-1","extra":true}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}
}
