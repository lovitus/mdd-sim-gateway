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
