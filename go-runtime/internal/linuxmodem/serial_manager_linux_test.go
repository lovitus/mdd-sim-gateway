//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
)

func TestSerialModeCannotRunBesideModemManagerOrCreateDataBearer(t *testing.T) {
	want := errors.New("ModemManager still running")
	manager := &serialManager{ensureStopped: func(context.Context) error { return want }}
	if _, err := manager.Inventory(context.Background()); !errors.Is(err, want) {
		t.Fatal("serial discovery bypassed service ownership", err)
	}
	if err := manager.Inhibit(context.Background(), "device", true); !errors.Is(err, want) {
		t.Fatal("serial ownership bypassed service check", err)
	}
	if _, err := manager.Connect(context.Background(), "", agentdata.Profile{}); err == nil {
		t.Fatal("serial mode created a data bearer")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSerialModeProfilesRetainOriginalDefaultInterface(t *testing.T) {
	profiles, err := parseSerialProfiles([]byte(`[{"vid":"2c7c","pid":"0125","name":"EC25"}]`))
	if err != nil || len(profiles) != 1 || profiles[0].ATInterface != nil {
		t.Fatal("original omitted-interface profile rejected", err)
	}
	for _, payload := range []string{"", `null`, `[]`} {
		profiles, err := parseSerialProfiles([]byte(payload))
		if err != nil || len(profiles) != 0 {
			t.Fatalf("empty legacy profile list rejected: %v", err)
		}
	}
	for _, payload := range []string{`{}`, `[{"vid":"bad","pid":"0125"}]`, `[{"vid":"2c7c","pid":"0125"},{"vid":"2C7C","pid":"0125"}]`} {
		if _, err := parseSerialProfiles([]byte(payload)); err == nil {
			t.Fatal("invalid profiles accepted")
		}
	}
}
