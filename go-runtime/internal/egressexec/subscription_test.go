package egressexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
)

const testSubscription = `proxies:
  - name: GB London
    type: ss
    server: 192.0.2.1
    port: 8388
    cipher: aes-128-gcm
    password: fixture
  - name: GB Manchester
    type: trojan
    server: 192.0.2.2
    port: 443
    password: fixture
  - name: quota 58.5GB
    type: ss
    server: 192.0.2.3
    port: 8388
    cipher: aes-128-gcm
    password: fixture
`

func TestSubscriptionPreservesSelectionAndCountryTokenBoundary(t *testing.T) {
	nodes, err := parseSubscription([]byte(testSubscription))
	if err != nil {
		t.Fatal(err)
	}
	document := egressDocument(strings.Repeat("a", 64), "")
	document.Proxy.Profiles["node"] = egressconfig.Profile{Type: "subscription"}
	exit := document.Proxy.Exits["gb"]
	exit.Keywords = []string{"GB"}
	document.Proxy.Exits["gb"] = exit
	rendered, err := renderAtBase(document, 22000, map[string][]subscriptionNode{"node": nodes}, map[string]string{"gb": "GB Manchester"})
	if err != nil {
		t.Fatal(err)
	}
	status := rendered.Status.Exits["gb"]
	if status.Node != "GB Manchester" || status.CandidateCount != 2 {
		t.Fatal(status)
	}
	var config struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(rendered.Config, &config); err != nil {
		t.Fatal(err)
	}
	selector := config.Outbounds[len(config.Outbounds)-1]
	if selector["type"] != "selector" || selector["default"] != "exit-gb-1" || selector["interrupt_exist_connections"] != false {
		t.Fatal(selector)
	}
	if _, _, _, err := subscriptionPool(nodes, []string{"GB"}, "exit-gb", "missing", "lock", ""); err == nil {
		t.Fatal("missing locked node accepted")
	}
}

func TestSubscriptionCacheRetainsGoodFeedAndBacksOff(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(testSubscription))
	}))
	defer server.Close()
	cache := &subscriptionCache{root: t.TempDir(), client: server.Client()}
	profile := egressconfig.Profile{Type: "subscription", URL: server.URL, RefreshMinutes: 1}
	now := time.Now()
	nodes, err := cache.load(context.Background(), profile, now)
	if err != nil || len(nodes) != 3 {
		t.Fatal(len(nodes), err)
	}
	fail.Store(true)
	if nodes, err = cache.load(context.Background(), profile, now.Add(2*time.Minute)); err != nil || len(nodes) != 3 {
		t.Fatal("last good cache lost", err)
	}
	if _, err = cache.load(context.Background(), profile, now.Add(2*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests=%d, want initial and one failed refresh", calls.Load())
	}
}

func TestSubscriptionRejectsUnsafeConversion(t *testing.T) {
	for _, node := range []subscriptionNode{{Type: "ss", Plugin: "obfs"}, {Type: "vless", Network: "xhttp"}, {Type: "http"}} {
		if node.supportsUDP() {
			t.Fatal("unsupported transport accepted")
		}
	}
	node := subscriptionNode{Type: "vless", Name: "GB", Server: "192.0.2.1", Port: 443, TLS: true, UUID: "fixture", SkipCertVerify: true, Reality: map[string]string{"public-key": "fixture-key"}}
	out, err := node.outbound("test")
	if err != nil {
		t.Fatal(err)
	}
	tls := out["tls"].(map[string]any)
	if tls["insecure"] != false || tls["utls"].(map[string]any)["fingerprint"] != "chrome" {
		t.Fatal("REALITY fields lost")
	}
}

func TestSubscriptionRefreshDoesNotRestartHealthyPool(t *testing.T) {
	var feed atomic.Value
	feed.Store(testSubscription)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(feed.Load().(string))) }))
	defer server.Close()
	document := egressDocument(strings.Repeat("b", 64), "")
	document.Proxy.Profiles["node"] = egressconfig.Profile{Type: "subscription", URL: server.URL, RefreshMinutes: 1}
	runner, controller, _ := newTestExecutor(t, document)
	now := time.Now()
	runner.now = func() time.Time { return now }
	runner.reconcile(context.Background())
	if controller.starts != 1 {
		t.Fatalf("starts=%d", controller.starts)
	}
	runner.reconcile(context.Background())
	if controller.starts != 1 || controller.processes[0].signals != 0 {
		t.Fatal("unchanged subscription restarted process")
	}
	feed.Store(strings.ReplaceAll(testSubscription, "GB London", "GB New London"))
	now = now.Add(2 * time.Minute)
	runner.reconcile(context.Background())
	if controller.starts != 1 || controller.processes[0].stops != 0 || controller.processes[0].signals != 0 {
		t.Fatal("background refresh interrupted healthy pool")
	}
	document.Generation = strings.Repeat("c", 64)
	document.RefreshID = "explicit-drained-apply"
	writeEgressDocument(t, runner.settings.DesiredPath, document)
	runner.reconcile(context.Background())
	if controller.starts != 1 || controller.processes[0].signals != 1 || controller.processes[0].stops != 0 ||
		runner.applied != document.Generation || runner.appliedResult.Status.Exits["gb"].Node != "GB Manchester" {
		t.Fatal("explicit apply did not activate refreshed pool")
	}
}
