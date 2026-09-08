package egressexec

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
)

const xhttpFixture = `proxies:
  - name: GB XHTTP
    type: vless
    network: xhttp
    server: 192.0.2.10
    port: 443
    uuid: 00000000-0000-4000-8000-000000000001
    packet-encoding: xudp
    servername: example.invalid
    client-fingerprint: firefox
    reality-opts:
      public-key: fixture-public-key
      short-id: abcd
    xhttp-opts:
      host: front.example.invalid
      path: /transport
      mode: packet-up
      extra:
        scMaxEachPostBytes: 1000000
        headers:
          X-Fixture: preserved
`

func xhttpNode(t *testing.T) subscriptionNode {
	t.Helper()
	nodes, err := parseSubscription([]byte(xhttpFixture))
	if err != nil || len(nodes) != 1 {
		t.Fatal("fixture parse", err)
	}
	return nodes[0]
}

func TestXHTTPManualLinkSharesNativeConversionAndFirstHopRule(t *testing.T) {
	link := "vless://00000000-0000-4000-8000-000000000001@192.0.2.10:443?security=reality&type=xhttp&pbk=fixture-key&path=%2Ftransport&extra=" + url.QueryEscape(`{"scMaxEachPostBytes":1000000}`)
	node, err := parseVLESSLink(link)
	if err != nil || node.XHTTP.Path != "/transport" || node.PacketEncoding != "xudp" || node.Fingerprint != "chrome" {
		t.Fatal("manual defaults differ", err)
	}
	profile := egressconfig.Profile{Type: "node", Value: link + "\nss://aes-128-gcm:fixture@192.0.2.11:8388"}
	bridges := &xhttpBridges{}
	out, _, err := renderProfileWithBridges(profile, "manual", "exit", bridges)
	if err != nil || len(out) != 2 || out[1]["detour"] != "exit-hop-1" || len(bridges.outbounds) != 1 {
		t.Fatal("manual chain not wired", err)
	}
	profile.Value = "ss://aes-128-gcm:fixture@192.0.2.11:8388\n" + link
	if _, _, err := renderProfileWithBridges(profile, "manual", "exit", &xhttpBridges{}); err == nil {
		t.Fatal("non-first XHTTP hop accepted contrary to original contract")
	}
	if _, err := parseVLESSLink(link + "%7B"); err == nil {
		t.Fatal("malformed transport options discarded")
	}
	isolated := &xhttpBridges{allocatePort: func() (int, error) { return 30001, nil }}
	if out, err := isolated.outbound(node, "probe", "probe"); err != nil || out["server_port"] != 30001 {
		t.Fatal("probe used production bridge port range", err)
	}
}

func TestXHTTPPreservesOriginalNativeContract(t *testing.T) {
	node := xhttpNode(t)
	out, err := node.xrayOutbound("test")
	if err != nil {
		t.Fatal(err)
	}
	stream := out["streamSettings"].(map[string]any)
	if out["protocol"] != "vless" || stream["network"] != "xhttp" || stream["security"] != "reality" {
		t.Fatal("transport changed")
	}
	xhttp := stream["xhttpSettings"].(map[string]any)
	reality := stream["realitySettings"].(map[string]any)
	if xhttp["path"] != "/transport" || xhttp["mode"] != "packet-up" || xhttp["host"] != "front.example.invalid" ||
		reality["serverName"] != "example.invalid" || reality["fingerprint"] != "firefox" || reality["shortId"] != "abcd" {
		t.Fatal("original options lost")
	}
	user := out["settings"].(map[string]any)["vnext"].([]map[string]any)[0]["users"].([]map[string]any)[0]
	if user["packetEncoding"] != "xudp" || user["id"] != node.UUID || user["encryption"] != "none" {
		t.Fatal("user contract lost")
	}
	extra := xhttp["extra"].(map[string]any)
	if extra["scMaxEachPostBytes"] != float64(1000000) || extra["headers"].(map[string]any)["X-Fixture"] != "preserved" {
		t.Fatal("nested extra options lost")
	}
	extra["headers"].(map[string]any)["X-Fixture"] = "changed"
	second, _ := node.xrayOutbound("again")
	payload, _ := json.Marshal(second)
	if strings.Contains(string(payload), "changed") {
		t.Fatal("renderer mutated source options")
	}
	node.XHTTP, node.ServerName, node.Fingerprint = xhttpOptions{}, "", ""
	out, err = node.xrayOutbound("defaults")
	if err != nil {
		t.Fatal(err)
	}
	stream = out["streamSettings"].(map[string]any)
	if stream["xhttpSettings"].(map[string]any)["mode"] != "auto" || stream["xhttpSettings"].(map[string]any)["path"] != "/" ||
		stream["realitySettings"].(map[string]any)["fingerprint"] != "chrome" || stream["realitySettings"].(map[string]any)["serverName"] != node.Server {
		t.Fatal("original defaults lost")
	}
}

func TestXHTTPBridgeIsLoopbackStableAndBounded(t *testing.T) {
	node := xhttpNode(t)
	bridges := &xhttpBridges{}
	first, err := bridges.outbound(node, "sing-one", "profile/GB XHTTP")
	if err != nil {
		t.Fatal(err)
	}
	second, err := bridges.outbound(node, "sing-two", "profile/GB XHTTP")
	if err != nil || first["server_port"] != second["server_port"] || len(bridges.inbounds) != 1 {
		t.Fatal("same endpoint was duplicated", err)
	}
	if first["server"] != "127.0.0.1" || first["type"] != "socks" || bridges.inbounds[0]["listen"] != "127.0.0.1" ||
		bridges.inbounds[0]["settings"].(map[string]any)["udp"] != true {
		t.Fatal("bridge is not loopback UDP SOCKS")
	}
	if payload, err := bridges.config(); err != nil || !json.Valid(payload) {
		t.Fatal("invalid configuration", err)
	}
	collision := &xhttpBridges{reserved: map[int]bool{first["server_port"].(int): true}}
	third, err := collision.outbound(node, "sing", "profile/GB XHTTP")
	if err != nil || third["server_port"] == first["server_port"] {
		t.Fatal("reserved port reused", err)
	}
	full := &xhttpBridges{reserved: map[int]bool{}}
	for port := 24000; port < 25000; port++ {
		full.reserved[port] = true
	}
	if _, err := full.outbound(node, "sing", "other"); err == nil || len(full.inbounds) != 0 {
		t.Fatal("exhausted bridge accepted")
	}
}

func TestXHTTPRejectsIncompleteContractWithoutPlainTCPFallback(t *testing.T) {
	for _, mutate := range []func(*subscriptionNode){
		func(n *subscriptionNode) { n.Type = "trojan" },
		func(n *subscriptionNode) { n.Network = "tcp" },
		func(n *subscriptionNode) { n.Reality = nil },
		func(n *subscriptionNode) { n.UUID = "" },
		func(n *subscriptionNode) { n.Port = 0 },
		func(n *subscriptionNode) { disabled := false; n.UDP = &disabled },
		func(n *subscriptionNode) { n.XHTTP.Extra = map[string]any{"invalid": make(chan int)} },
	} {
		node := xhttpNode(t)
		mutate(&node)
		bridges := &xhttpBridges{}
		if _, err := bridges.outbound(node, "test", "test"); err == nil || len(bridges.inbounds) != 0 {
			t.Fatal("invalid contract changed bridge state")
		}
	}
	node := xhttpNode(t)
	if _, err := node.outbound("test"); err == nil {
		t.Fatal("XHTTP incorrectly converted to native sing-box")
	}
}
