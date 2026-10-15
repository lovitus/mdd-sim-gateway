package egressexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/scopedtoken"
)

func TestCellularClientDefersTokenReadAndRecoversWithoutReconstruction(t *testing.T) {
	root := t.TempDir()
	tokenPath := filepath.Join(root, "token")
	var mu sync.Mutex
	expectedToken := ""
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if request.Header.Get("Authorization") != "Bearer "+expectedToken {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		requests++
		var input struct {
			ExpiresAt time.Time `json:"expires_at"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"session_id": "session-a", "line_id": "line-a", "state": "ready", "profile": "carrier",
			"purpose": "egress:sim-gb", "listen_port": 32123, "username": "user-a", "password": "secret-a",
			"expires_at": input.ExpiresAt, "max_bytes": uint64(1 << 40), "used_bytes": 0,
		})
	}))
	defer server.Close()

	client, err := newCellularClient(server.URL, tokenPath)
	if err != nil {
		t.Fatalf("construct before Core creates token: %v", err)
	}
	if _, err = client.prepare(t.Context(), egressconfig.Config{}); err != nil {
		t.Fatalf("non-cellular configuration depended on token: %v", err)
	}
	config := egressconfig.Config{Enabled: true,
		Profiles: map[string]egressconfig.Profile{
			"sim-gb": {Type: "cellular_sim", SIMICCID: "8985200000000000001"},
		},
		Exits: map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "sim-gb"}},
	}
	if degraded, err := client.prepare(t.Context(), config); err != nil || degraded.Profiles["sim-gb"].RuntimeError == "" {
		t.Fatalf("missing token was not reported on its profile: %v", err)
	}
	mu.Lock()
	if requests != 0 {
		t.Fatalf("request escaped before token existed: %d", requests)
	}
	mu.Unlock()

	token, err := scopedtoken.Ensure(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	expectedToken = token
	mu.Unlock()
	if _, err = client.prepare(t.Context(), config); err != nil {
		t.Fatalf("same client did not recover after Core created token: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("requests after token creation=%d", requests)
	}
}

func TestCellularClientReadsCurrentTokenForEveryRequest(t *testing.T) {
	root := t.TempDir()
	tokenPath := filepath.Join(root, "token")
	first, err := scopedtoken.Ensure(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	expectedToken := first
	authorizations := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		if request.Header.Get("Authorization") != "Bearer "+expectedToken {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodDelete {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		var input struct {
			ExpiresAt time.Time `json:"expires_at"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"session_id": "session-a", "line_id": "line-a", "state": "ready", "profile": "carrier",
			"purpose": "egress:sim-gb", "listen_port": 32123, "username": "user-a", "password": "secret-a",
			"expires_at": input.ExpiresAt, "max_bytes": uint64(1 << 40), "used_bytes": 0,
		})
	}))
	defer server.Close()
	client, err := newCellularClient(server.URL, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	config := egressconfig.Config{Enabled: true,
		Profiles: map[string]egressconfig.Profile{
			"sim-gb": {Type: "cellular_sim", SIMICCID: "8985200000000000001"},
		},
		Exits: map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "sim-gb"}},
	}
	if _, err = client.prepare(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	second := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	if err := os.WriteFile(tokenPath, []byte(second+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	expectedToken = second
	mu.Unlock()
	disabled := config
	disabled.Enabled = false
	if _, err = client.prepare(t.Context(), disabled); err != nil {
		t.Fatalf("stop did not use rotated token: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(authorizations) != 2 || authorizations[0] != "Bearer "+first || authorizations[1] != "Bearer "+second {
		t.Fatalf("authorization sequence=%v", authorizations)
	}
}

func TestCellularProfileBecomesRuntimeSOCKSAndRenewsOneSession(t *testing.T) {
	root := t.TempDir()
	tokenPath := filepath.Join(root, "token")
	token, err := scopedtoken.Ensure(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	posts, deletes := 0, 0
	port := 32123
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if request.Method == http.MethodDelete {
			deletes++
			response.WriteHeader(http.StatusNoContent)
			return
		}
		posts++
		var input struct {
			ExpiresAt time.Time `json:"expires_at"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		_ = json.NewEncoder(response).Encode(map[string]any{"session_id": "session-a", "line_id": "line-a", "state": "ready",
			"profile": "carrier", "purpose": "egress:gb", "listen_port": port, "username": "user-a", "password": "secret-a",
			"expires_at": input.ExpiresAt, "max_bytes": uint64(1 << 40), "used_bytes": 0})
	}))
	defer server.Close()
	client, err := newCellularClient(server.URL, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	client.now = func() time.Time { return now }
	config := egressconfig.Config{SchemaVersion: 2, Enabled: true, MissingPolicy: "error", RefreshMinutes: 30,
		Profiles: map[string]egressconfig.Profile{"sim-gb": {Name: "Data SIM", Type: "cellular_sim", SIMICCID: "8985200000000000001"}},
		Exits:    map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "sim-gb"}}}
	runtimeConfig, err := client.prepare(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	profile := runtimeConfig.Profiles["sim-gb"]
	if profile.Type != "socks5" || profile.Server != "127.0.0.1" || profile.Port != 32123 || profile.Password != "secret-a" {
		t.Fatalf("runtime profile=%+v", profile)
	}
	if config.Profiles["sim-gb"].Type != "cellular_sim" || config.Profiles["sim-gb"].Password != "" {
		t.Fatal("desired config was mutated with runtime credentials")
	}
	if _, err = client.prepare(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count := posts
	mu.Unlock()
	if count != 1 {
		t.Fatalf("early renew posts=%d", count)
	}
	now = now.Add(cellularRenewEvery + time.Second)
	if _, err = client.prepare(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count = posts
	mu.Unlock()
	if count != 2 {
		t.Fatalf("renew posts=%d", count)
	}
	document := egressdesired.Document{Version: 2, Generation: strings.Repeat("a", 64), Proxy: runtimeConfig}
	rendered, err := RenderAtBase(document, 24000)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := json.Marshal(rendered.Status)
	if rendered.Status.Exits["gb"].Mode != "cellular_sim" || !rendered.Status.Exits["gb"].Ready {
		t.Fatalf("cellular exit status=%+v", rendered.Status.Exits["gb"])
	}
	if strings.Contains(string(status), "secret-a") || strings.Contains(string(status), "user-a") {
		t.Fatal("runtime credentials leaked into status")
	}
	// Same saved generation, different temporary SOCKS route: do not silently
	// advertise the new port while the shared process still runs the old one.
	document.Proxy = config
	document.AttemptID = "initial-apply"
	runner, controller, statusPath := newTestExecutor(t, document)
	runner.cellular = client
	runner.reconcile(context.Background())
	mu.Lock()
	port = 32124
	mu.Unlock()
	now = now.Add(cellularRenewEvery + time.Second)
	runner.reconcile(context.Background())
	pending := readExecutorStatus(t, statusPath)
	if !pending.PendingApply || pending.RuntimeConfirmed || pending.Exits["gb"].Ready || controller.starts != 1 {
		t.Fatal("temporary route escaped coordinated apply", pending)
	}
	document.AttemptID = "explicit-drained-apply"
	writeEgressDocument(t, runner.settings.DesiredPath, document)
	runner.reconcile(context.Background())
	confirmed := readExecutorStatus(t, statusPath)
	if !confirmed.RuntimeConfirmed || confirmed.AttemptID != document.AttemptID || controller.starts != 2 {
		t.Fatal("coordinated route update was not confirmed", confirmed)
	}
	// The shared executable can remain alive after its old donor disappears.
	// A rejected replacement must not renew the old route's readiness evidence.
	mu.Lock()
	port = 32125
	mu.Unlock()
	now = now.Add(cellularRenewEvery + time.Second)
	document.AttemptID = "rejected-route-apply"
	writeEgressDocument(t, runner.settings.DesiredPath, document)
	controller.checkError = errors.New("candidate rejected")
	for range 2 {
		runner.reconcile(context.Background())
		rejected := readExecutorStatus(t, statusPath)
		if !rejected.RuntimeConfirmed || rejected.Outcome != "unavailable" || rejected.Exits["gb"].Ready || rejected.Exits["gb"].ProxyPort != 0 || controller.starts != 2 || runner.child != nil {
			t.Fatal("rejected route did not reach confirmed stopped state without revoked donor readiness", rejected)
		}
	}
	disabled := config
	disabled.Enabled = false
	if _, err = client.prepare(t.Context(), disabled); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	removed := deletes
	mu.Unlock()
	if removed != 1 {
		t.Fatalf("lease deletes=%d", removed)
	}
}

func TestCellularKeepaliveOutlivesSlowSiblingPrepare(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if _, err := scopedtoken.Ensure(tokenPath); err != nil {
		t.Fatal(err)
	}
	started, release, renewed := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var posts int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			CardID    string    `json:"card_id"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.CardID == "8985200000000000002" {
			close(started)
			<-release
			w.WriteHeader(503)
			return
		}
		mu.Lock()
		posts++
		n := posts
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "healthy-owner", "line_id": "healthy-line", "state": "ready", "purpose": "egress:z-healthy",
			"listen_port": 32123, "username": "user", "password": "secret", "expires_at": input.ExpiresAt, "max_bytes": cellularMaxBytes, "used_bytes": 0})
		if n > 1 {
			select {
			case renewed <- struct{}{}:
			default:
			}
		}
	}))
	defer server.Close()
	defer unblock()
	client, err := newCellularClient(server.URL, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	healthy := egressconfig.Profile{Type: "cellular_sim", SIMICCID: "8985200000000000001"}
	lease, err := client.ensure(t.Context(), "z-healthy", healthy)
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	lease.nextRenew = time.Now().Add(-time.Second)
	client.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { client.keepAlive(ctx); close(done) }()
	defer func() { cancel(); unblock(); <-done }()
	prepared := make(chan struct{})
	go func() {
		_, _ = client.prepare(ctx, egressconfig.Config{Enabled: true, Profiles: map[string]egressconfig.Profile{
			"a-slow": {Type: "cellular_sim", SIMICCID: "8985200000000000002"}, "z-healthy": healthy},
			Exits: map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "z-healthy"}, "hk": {Enabled: true, ProfileID: "a-slow"}}})
		close(prepared)
	}()
	defer func() { unblock(); <-prepared }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow preparation was not admitted")
	}
	select {
	case <-renewed:
	case <-time.After(3 * time.Second):
		t.Fatal("healthy lease renewal was blocked by slow sibling")
	}
	// The server signal precedes response decoding; wait for the renewal commit.
	lock := client.profileLock("z-healthy")
	lock.Lock()
	client.mu.Lock()
	current := client.leases["z-healthy"]
	client.mu.Unlock()
	lock.Unlock()
	if current.SessionID != lease.SessionID || current.ListenPort != lease.ListenPort || current.Password != lease.Password || !current.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatal("healthy owner was replaced or not extended", current)
	}
}

func TestCellularPrepareFailurePreservesUnrelatedLeaseAndRenewal(t *testing.T) {
	root := t.TempDir()
	tokenPath := filepath.Join(root, "token")
	token, err := scopedtoken.Ensure(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	posts := map[string]int{}
	deletes := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodDelete {
			var input struct {
				SessionID string `json:"session_id"`
			}
			_ = json.NewDecoder(request.Body).Decode(&input)
			mu.Lock()
			deletes = append(deletes, input.SessionID)
			mu.Unlock()
			response.WriteHeader(http.StatusNoContent)
			return
		}
		var input struct {
			CardID    string    `json:"card_id"`
			Purpose   string    `json:"purpose"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		mu.Lock()
		posts[input.Purpose]++
		mu.Unlock()
		if input.CardID == "8985200000000000002" {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"session_id": "session-a", "line_id": "line-a", "state": "ready", "profile": "carrier",
			"purpose": input.Purpose, "listen_port": 32123, "username": "user-a", "password": "secret-a",
			"expires_at": input.ExpiresAt, "max_bytes": uint64(1 << 40), "used_bytes": 0,
		})
	}))
	defer server.Close()
	client, err := newCellularClient(server.URL, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	client.now = func() time.Time { return now }
	base := egressconfig.Config{SchemaVersion: 2, Enabled: true, MissingPolicy: "error", RefreshMinutes: 30,
		Profiles: map[string]egressconfig.Profile{
			"a": {Name: "Working SIM", Type: "cellular_sim", SIMICCID: "8985200000000000001"},
		}, Exits: map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "a"}}}
	if _, err := client.prepare(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	failing := base
	failing.Profiles = map[string]egressconfig.Profile{
		"a": base.Profiles["a"],
		"b": {Name: "Failed SIM", Type: "cellular_sim", SIMICCID: "8985200000000000002"},
	}
	failing.Exits = map[string]egressconfig.Exit{
		"gb": {Enabled: true, ProfileID: "a"}, "fr": {Enabled: true, ProfileID: "b"},
	}
	prepared, err := client.prepare(t.Context(), failing)
	if err != nil || prepared.Profiles["b"].RuntimeError == "" || prepared.Profiles["a"].Type != "socks5" {
		t.Fatal("per-profile failure was not isolated", err)
	}
	mu.Lock()
	deleted := append([]string(nil), deletes...)
	initialAPosts := posts["egress:a"]
	mu.Unlock()
	if len(deleted) != 0 || len(client.leases) != 1 {
		t.Fatalf("failed profile disrupted healthy lease deletes=%v leases=%v", deleted, client.leases)
	}
	now = now.Add(cellularRenewEvery + time.Second)
	if _, err := client.prepare(t.Context(), failing); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	aPosts := posts["egress:a"]
	mu.Unlock()
	if aPosts != initialAPosts+1 || len(client.leases) != 1 || len(deletes) != 0 {
		t.Fatalf("healthy lease renewal was blocked: posts %d->%d leases=%v",
			initialAPosts, aPosts, client.leases)
	}
}
