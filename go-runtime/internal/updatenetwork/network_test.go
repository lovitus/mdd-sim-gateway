package updatenetwork

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
)

func TestUpdateCandidatesPreserveExplicitSelectionAndExcludeMeteredFallback(t *testing.T) {
	config := egressconfig.Snapshot{Revision: 7, Config: egressconfig.Config{Enabled: true, Profiles: map[string]egressconfig.Profile{
		"socks": {Type: "socks5", Server: "2001:db8::1", Port: 1080, Username: "fixture-user", Password: "fixture-secret"},
		"node":  {Type: "node"}, "data": {Type: "cellular_sim"},
	}, Exits: map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "node"}}}}
	actual := egressstatus.Snapshot{DesiredGeneration: strings.Repeat("a", 64), Exits: map[string]egressstatus.Exit{"gb": {Ready: true, HostProxyHost: "127.0.0.1", ProxyPort: 22001}}}
	routes, err := Candidates(Selection{Mode: "auto"}, config, actual, actual.DesiredGeneration)
	if err != nil || len(routes) != 3 || routes[0].Mode != "direct" || routes[1].ProfileID != "node" || routes[2].ProfileID != "socks" {
		t.Fatal("unexpected update routes", err)
	}
	wire, err := json.Marshal(routes)
	if err != nil || strings.Contains(string(wire), "fixture-secret") || strings.Contains(string(wire), "fixture-user") {
		t.Fatal("route identity serialized credentials")
	}
	client, err := routes[2].Client(12 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := client.Transport.(*http.Transport).Proxy(&http.Request{})
	if err != nil || proxy.Host != "[2001:db8::1]:1080" {
		t.Fatal("invalid IPv6 proxy routing", err)
	}
	if _, err := Candidates(Selection{Mode: "library", ProfileID: "data"}, config, actual, actual.DesiredGeneration); err == nil {
		t.Fatal("metered route implicitly authorized")
	}
	if _, err := Candidates(Selection{Mode: "library", ProfileID: "missing"}, config, actual, actual.DesiredGeneration); err == nil {
		t.Fatal("missing explicit proxy fell back to direct")
	}
	if _, err := Candidates(Selection{Mode: "library", ProfileID: "node"}, config, actual, "other"); err == nil {
		t.Fatal("stale exit accepted")
	}
	client, err = routes[0].Client(0)
	if err != nil || client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("direct route used environment proxy")
	}
	var restored []Route
	if err := json.Unmarshal(wire, &restored); err != nil {
		t.Fatal(err)
	}
	if _, err := restored[1].Client(time.Second); err == nil {
		t.Fatal("persisted identity became an unfenced transport")
	}
	resolved, err := ResolveRecorded(restored[1], config, actual)
	if err != nil || resolved.Country != "gb" {
		t.Fatal("verified identity could not be re-resolved", err)
	}
	config.Config.Exits["fr"] = egressconfig.Exit{Enabled: true, ProfileID: "node"}
	actual.Exits["fr"] = egressstatus.Exit{Ready: true, HostProxyHost: "127.0.0.1", ProxyPort: 22002}
	if same, err := ResolveRecorded(restored[1], config, actual); err != nil || same.Country != "gb" {
		t.Fatal("another ready exit displaced the verified route", err)
	}
	config.Revision++
	if _, err := ResolveRecorded(restored[1], config, actual); err == nil {
		t.Fatal("changed proxy configuration accepted")
	}
	if _, err := ResolveRecorded(Route{Mode: "auto"}, config, actual); err == nil {
		t.Fatal("unresolved auto mode accepted as verified route")
	}
}
