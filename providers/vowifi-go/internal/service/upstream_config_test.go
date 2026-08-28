// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"errors"
	"strings"
	"testing"

	upstreamswu "github.com/boa-z/vowifi-go/engine/swu"
	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
)

func TestNewUpstreamFactoryValidatesSOCKS5Proxy(t *testing.T) {
	config := UpstreamConfig{
		LineID: "line-1", DeviceID: "device-1",
		Profile:   identity.Profile{IMSI: "234100000000001"},
		BrokerURL: "http://127.0.0.1:39002/v1/agent/aka", BrokerToken: strings.Repeat("a", 32),
		ProxyURL: "socks5://127.0.0.1:1080",
	}
	if _, err := NewUpstreamFactory(config); err != nil {
		t.Fatal(err)
	}
	for _, proxy := range []string{
		"http://127.0.0.1:1080", "socks5://127.0.0.1", "socks5://user@127.0.0.1:1080",
		"socks5://127.0.0.1:0", "socks5://127.0.0.1:70000",
	} {
		config.ProxyURL = proxy
		if _, err := NewUpstreamFactory(config); err == nil {
			t.Fatalf("invalid proxy %q was accepted", proxy)
		}
	}
}

func TestNewUpstreamFactoryNormalizesPDNFamily(t *testing.T) {
	base := UpstreamConfig{
		LineID: "line-1", DeviceID: "device-1",
		Profile:   identity.Profile{IMSI: "234100000000001"},
		BrokerURL: "http://127.0.0.1:39002/v1/agent/aka", BrokerToken: strings.Repeat("a", 32),
	}
	for _, family := range []string{"", "v4", "v6", "dual", " V6 "} {
		base.PDNFamily = family
		factory, err := NewUpstreamFactory(base)
		if err != nil {
			t.Fatalf("family %q: %v", family, err)
		}
		want := strings.ToLower(strings.TrimSpace(family))
		if want == "" {
			want = "v6"
		}
		if factory.config.PDNFamily != want {
			t.Fatalf("family %q normalized to %q, want %q", family, factory.config.PDNFamily, want)
		}
	}
	base.PDNFamily = "auto"
	if _, err := NewUpstreamFactory(base); err == nil {
		t.Fatal("unsupported automatic PDN family was accepted")
	}
}

func TestNewUpstreamFactoryNormalizesIMSAPN(t *testing.T) {
	base := UpstreamConfig{
		LineID: "line-1", DeviceID: "device-1",
		Profile:   identity.Profile{IMSI: "234100000000001"},
		BrokerURL: "http://127.0.0.1:39002/v1/agent/aka", BrokerToken: strings.Repeat("a", 32),
	}
	factory, err := NewUpstreamFactory(base)
	if err != nil {
		t.Fatal(err)
	}
	if factory.config.IMSAPN != "ims" {
		t.Fatalf("default IMS APN=%q, want ims", factory.config.IMSAPN)
	}
	base.IMSAPN = " IMS-CUSTOM "
	factory, err = NewUpstreamFactory(base)
	if err != nil {
		t.Fatal(err)
	}
	if factory.config.IMSAPN != "ims-custom" {
		t.Fatalf("normalized IMS APN=%q, want ims-custom", factory.config.IMSAPN)
	}
}

func TestIMSRegisterDiagnosticPreservesCauseAndPacketEvidence(t *testing.T) {
	cause := errors.New("register timeout")
	err := imsRegisterDiagnostic(cause, []string{" 2001:db8::5 ", ""}, upstreamswu.PacketTunnelStats{
		OutboundInnerPackets: 3, OutboundESPPackets: 3, InboundESPPackets: 1, InvalidDrops: 1,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("diagnostic lost cause: %v", err)
	}
	want := "P-CSCF candidates 2001:db8::5; SWu packets"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("diagnostic=%q, want %q", err, want)
	}
}

func TestSWUPDNConfigurationKeepsCPAndTrafficSelectorsInSameFamily(t *testing.T) {
	tests := []struct {
		family    string
		wantAttrs []uint16
		wantTS    []uint8
	}{
		{family: "v4", wantAttrs: []uint16{ikev2.ConfigInternalIPv4Address, ikev2.ConfigInternalIPv4DNS, ikev2.ConfigPCSCFIPv4Address}, wantTS: []uint8{ikev2.TSIPv4AddressRange}},
		{family: "v6", wantAttrs: []uint16{ikev2.ConfigInternalIPv6Address, ikev2.ConfigInternalIPv6DNS, ikev2.ConfigPCSCFIPv6Address}, wantTS: []uint8{ikev2.TSIPv6AddressRange}},
		{family: "dual", wantAttrs: []uint16{ikev2.ConfigInternalIPv4Address, ikev2.ConfigInternalIPv4DNS, ikev2.ConfigPCSCFIPv4Address, ikev2.ConfigInternalIPv6Address, ikev2.ConfigInternalIPv6DNS, ikev2.ConfigPCSCFIPv6Address}, wantTS: []uint8{ikev2.TSIPv4AddressRange, ikev2.TSIPv6AddressRange}},
	}
	for _, tt := range tests {
		configuration, selectors := swuPDNConfiguration(tt.family)
		if len(configuration.Attributes) != len(tt.wantAttrs) || len(selectors.Selectors) != len(tt.wantTS) {
			t.Fatalf("family %s configuration=%+v selectors=%+v", tt.family, configuration, selectors)
		}
		for i, want := range tt.wantAttrs {
			if configuration.Attributes[i].Type != want {
				t.Fatalf("family %s attribute %d=%d, want %d", tt.family, i, configuration.Attributes[i].Type, want)
			}
		}
		for i, want := range tt.wantTS {
			if selectors.Selectors[i].Type != want {
				t.Fatalf("family %s selector %d=%d, want %d", tt.family, i, selectors.Selectors[i].Type, want)
			}
		}
	}
}
