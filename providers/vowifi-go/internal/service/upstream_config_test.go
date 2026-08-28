// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"strings"
	"testing"

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
