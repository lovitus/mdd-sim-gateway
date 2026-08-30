package egressexec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
)

func egressDocument(generation, node string) egressdesired.Document {
	return egressdesired.Document{
		Version: 2, Generation: generation,
		Proxy: egressconfig.Config{SchemaVersion: 2, Enabled: true, Profiles: map[string]egressconfig.Profile{
			"node": {Name: "London", Type: "node", Value: node},
		}, Exits: map[string]egressconfig.Exit{"gb": {Enabled: true, ProfileID: "node"}}},
	}
}

func TestRenderProductionStyleShadowsocksAsLoopbackUDPProxy(t *testing.T) {
	document := egressDocument(strings.Repeat("a", 64), "ss://chacha20-ietf-poly1305:password@192.0.2.10:8389")
	rendered, err := Render(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Ports) != 1 || rendered.Ports[0] != 22157 {
		t.Fatalf("ports=%v", rendered.Ports)
	}
	exit := rendered.Status.Exits["gb"]
	if !exit.Ready || exit.HostProxyHost != "127.0.0.1" || exit.ProxyPort != 22157 || exit.Node != "London" {
		t.Fatalf("exit=%+v", exit)
	}
	var config struct {
		Inbounds  []map[string]any `json:"inbounds"`
		Outbounds []map[string]any `json:"outbounds"`
		Route     map[string]any   `json:"route"`
	}
	if err := json.Unmarshal(rendered.Config, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 1 || config.Inbounds[0]["listen"] != "127.0.0.1" ||
		len(config.Outbounds) != 1 || config.Outbounds[0]["type"] != "shadowsocks" ||
		config.Outbounds[0]["udp_fragment"] != true || config.Route["default_domain_resolver"] != "dns-bootstrap" {
		t.Fatalf("config=%s", rendered.Config)
	}
}

func TestRenderLinearChainUsesOnlyExplicitDetours(t *testing.T) {
	document := egressDocument(strings.Repeat("b", 64),
		"socks5://first.example:1080\nss://aes-128-gcm:secret@192.0.2.20:8388")
	rendered, err := Render(document)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(rendered.Config, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Outbounds) != 2 || config.Outbounds[0]["tag"] != "exit-gb-hop-1" ||
		config.Outbounds[1]["tag"] != "exit-gb" || config.Outbounds[1]["detour"] != "exit-gb-hop-1" {
		t.Fatalf("outbounds=%v", config.Outbounds)
	}
}

func TestRenderRejectsUnimplementedProfilesInsteadOfFallingBackDirect(t *testing.T) {
	for _, kind := range []string{"subscription", "existing", "cellular_sim"} {
		document := egressDocument(strings.Repeat("c", 64), "ss://aes-128-gcm:secret@192.0.2.30:8388")
		profile := document.Proxy.Profiles["node"]
		profile.Type = kind
		document.Proxy.Profiles["node"] = profile
		if rendered, err := Render(document); err == nil || rendered.Status.Exits["gb"].Ready {
			t.Fatalf("kind=%s rendered=%+v err=%v", kind, rendered, err)
		}
	}
}
