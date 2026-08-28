package providerconfig

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigAllowsDynamicLiteralLoopbackOnly(t *testing.T) {
	var settings Config
	settings.LineID, settings.ProviderID, settings.DeviceID = "line-1", "native", "device-1"
	settings.IPC.Listen = "127.0.0.1:0"
	settings.IPC.Token = strings.Repeat("a", 32)
	settings.IPC.StatePath = filepath.Join(t.TempDir(), "operations.db")
	settings.Agent.BrokerToken = strings.Repeat("b", 32)
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	settings.IPC.Listen = "0.0.0.0:0"
	if err := settings.Validate(); err == nil {
		t.Fatal("non-loopback dynamic listener was accepted")
	}
}

func TestConfigAcceptsOnlyExactSOCKS5ProxyURL(t *testing.T) {
	var settings Config
	settings.LineID, settings.ProviderID, settings.DeviceID = "line-1", "native", "device-1"
	settings.IPC.Listen = "127.0.0.1:0"
	settings.IPC.Token = strings.Repeat("a", 32)
	settings.IPC.StatePath = filepath.Join(t.TempDir(), "operations.db")
	settings.Agent.BrokerToken = strings.Repeat("b", 32)
	for _, value := range []string{
		"socks5://127.0.0.1:1080",
		"socks5://user:password@proxy.example:1080",
	} {
		settings.Network.ProxyURL = value
		if err := settings.Validate(); err != nil {
			t.Fatalf("proxy %q: %v", value, err)
		}
	}
	for _, value := range []string{
		"http://127.0.0.1:1080", "socks5://127.0.0.1", "socks5://127.0.0.1:0",
		"socks5://user@127.0.0.1:1080", "socks5://127.0.0.1:1080/path",
		"socks5://127.0.0.1:1080?network=udp",
	} {
		settings.Network.ProxyURL = value
		if err := settings.Validate(); err == nil {
			t.Fatalf("invalid proxy %q was accepted", value)
		}
	}
}

func TestConfigAcceptsOnlyExplicitPDNFamilies(t *testing.T) {
	var settings Config
	settings.LineID, settings.ProviderID, settings.DeviceID = "line-1", "native", "device-1"
	settings.IPC.Listen = "127.0.0.1:0"
	settings.IPC.Token = strings.Repeat("a", 32)
	settings.IPC.StatePath = filepath.Join(t.TempDir(), "operations.db")
	settings.Agent.BrokerToken = strings.Repeat("b", 32)
	for _, family := range []string{"", "v4", "v6", "dual", " V6 "} {
		settings.Network.PDNFamily = family
		if err := settings.Validate(); err != nil {
			t.Fatalf("family %q: %v", family, err)
		}
	}
	settings.Network.PDNFamily = "auto"
	if err := settings.Validate(); err == nil {
		t.Fatal("unsupported automatic PDN family was accepted")
	}
}
