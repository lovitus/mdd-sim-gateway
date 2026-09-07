package egressexec

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExistingOutboundPreservesProtocolAndScopesDetour(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.json")
	payload := []byte(`{"inbounds":[{"type":"tun","tag":"never-import"}],"route":{"auto_detect_interface":true},"outbounds":[{"type":"socks","tag":"first","server":"192.0.2.1","server_port":1080,"version":"5"},{"type":"shadowsocks","tag":"chosen","server":"192.0.2.2","server_port":8388,"method":"aes-128-gcm","password":"fixture","udp_fragment":false,"detour":"first"},{"type":"http","tag":"unused","server":"192.0.2.3","server_port":80}]}`)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	document := egressDocument(strings.Repeat("a", 64), "")
	profile := document.Proxy.Profiles["node"]
	profile.Type = "existing"
	profile.OutboundTag = "chosen"
	document.Proxy.Profiles["node"] = profile
	document.Proxy.ExistingSingboxConfig = path
	document.ExistingConfigSHA256 = fmt.Sprintf("%x", sha256.Sum256(payload))
	result, err := Render(document)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Outbounds []map[string]any `json:"outbounds"`
		Inbounds  []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(result.Config, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Outbounds) != 2 || config.Outbounds[1]["tag"] != "exit-gb" || config.Outbounds[1]["detour"] != config.Outbounds[0]["tag"] || config.Outbounds[1]["udp_fragment"] != false {
		t.Fatalf("invalid dependency render: %v", config.Outbounds)
	}
	if len(config.Inbounds) != 1 || config.Inbounds[0]["type"] != "socks" || config.Inbounds[0]["listen"] != "127.0.0.1" {
		t.Fatal("source listener imported")
	}
	if result.Status.Exits["gb"].Node != "chosen" || !result.Status.Exits["gb"].Ready {
		t.Fatal(result.Status)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(payload) {
		t.Fatal("source configuration changed", err)
	}
	if err := os.WriteFile(path, append(payload, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := Render(document); err == nil || changed.Status.Exits["gb"].Ready {
		t.Fatal("unapplied source change accepted")
	}
}

func TestExistingOutboundRefusesUnsafeOrUnresolvedSelection(t *testing.T) {
	for name, payload := range map[string]string{
		"missing":        `{"outbounds":[]}`,
		"tcp":            `{"outbounds":[{"tag":"chosen","type":"socks","network":"tcp"}]}`,
		"socks4":         `{"outbounds":[{"tag":"chosen","type":"socks","version":"4"}]}`,
		"cycle":          `{"outbounds":[{"tag":"chosen","type":"socks","detour":"chosen"}]}`,
		"missing-detour": `{"outbounds":[{"tag":"chosen","type":"socks","detour":"missing"}]}`,
		"duplicate":      `{"outbounds":[{"tag":"chosen","type":"socks"},{"tag":"chosen","type":"socks"}]}`,
		"bad-json":       `{"outbounds":`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.json")
			if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
			if built, _, err := renderExisting(path, "chosen", "exit-gb", fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))); err == nil || len(built) != 0 {
				t.Fatal("invalid source accepted")
			}
		})
	}
}
