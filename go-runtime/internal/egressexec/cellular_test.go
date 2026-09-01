package egressexec

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/scopedtoken"
)

func TestCellularProfileBecomesRuntimeSOCKSAndRenewsOneSession(t *testing.T) {
	root := t.TempDir()
	tokenPath := filepath.Join(root, "token")
	token, err := scopedtoken.Ensure(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	posts, deletes := 0, 0
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
			"profile": "carrier", "purpose": "egress:gb", "listen_port": 32123, "username": "user-a", "password": "secret-a",
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
	if strings.Contains(string(status), "secret-a") || strings.Contains(string(status), "user-a") {
		t.Fatal("runtime credentials leaked into status")
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

func TestCellularPrepareFailureStopsEveryPreviouslyHeldLease(t *testing.T) {
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
	if _, err := client.prepare(t.Context(), failing); err == nil {
		t.Fatal("multi-profile prepare failure was hidden")
	}
	mu.Lock()
	deleted := append([]string(nil), deletes...)
	initialAPosts := posts["egress:a"]
	mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "session-a" || len(client.leases) != 0 {
		t.Fatalf("failed prepare retained leases deletes=%v leases=%v", deleted, client.leases)
	}
	if _, err := client.prepare(t.Context(), failing); err == nil {
		t.Fatal("repeated failing configuration unexpectedly succeeded")
	}
	mu.Lock()
	aPosts := posts["egress:a"]
	mu.Unlock()
	if aPosts != initialAPosts+1 || len(client.leases) != 0 {
		t.Fatalf("old lease was renewed instead of recreated then stopped: posts %d->%d leases=%v",
			initialAPosts, aPosts, client.leases)
	}
}
