//go:build !windows

package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerdeploy"
)

type hostSwitchFixture struct {
	states   map[string]providerdeploy.HostServiceState
	actions  []string
	failOnce string
}

func (fixture *hostSwitchFixture) HostModemServiceState(_ context.Context, unit string) (providerdeploy.HostServiceState, error) {
	return fixture.states[unit], nil
}
func (fixture *hostSwitchFixture) HostModemAction(_ context.Context, action, unit string) error {
	key := action + " " + unit
	fixture.actions = append(fixture.actions, key)
	if fixture.failOnce == key {
		fixture.failOnce = ""
		return errors.New("fixture action failure")
	}
	state := fixture.states[unit]
	switch action {
	case "start":
		state.ActiveState = "active"
		state.MainPID = 456
	case "stop":
		state.ActiveState = "inactive"
		state.MainPID = 0
	case "enable":
		state.UnitFileState = "enabled"
	case "disable":
		state.UnitFileState = "disabled"
	}
	fixture.states[unit] = state
	return nil
}
func newHostSwitchFixture() *hostSwitchFixture {
	return &hostSwitchFixture{states: map[string]providerdeploy.HostServiceState{
		"mdd-agent.service":    {LoadState: "loaded", ActiveState: "active", UnitFileState: "enabled", MainPID: 123},
		"ModemManager.service": {LoadState: "loaded", ActiveState: "active", UnitFileState: "enabled", MainPID: 124},
	}}
}

func TestHostModemSwitchPreservesOriginalSerialOrderAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		fixture := newHostSwitchFixture()
		if fail {
			fixture.failOnce = "start mdd-agent.service"
		}
		restored := 0
		verified := 0
		state, err := switchHostModemServices(context.Background(), "serial", fixture, func() error { restored++; return nil }, func(context.Context) error { verified++; return nil })
		if fail {
			if err == nil || state != "rolled_back" || restored != 1 || fixture.states["ModemManager.service"].ActiveState != "active" || fixture.states["ModemManager.service"].UnitFileState != "enabled" {
				t.Fatal("failed switch did not restore original services", state, err)
			}
		} else {
			want := []string{"stop mdd-agent.service", "stop ModemManager.service", "disable ModemManager.service", "start mdd-agent.service"}
			if err != nil || state != "applied" || restored != 0 || verified != 1 || !reflect.DeepEqual(fixture.actions, want) {
				t.Fatal("serial switch ordering changed", state, fixture.actions, err)
			}
		}
	}
}

func TestHostModemSwitchDoesNotClaimRecoveryWhenConfigRestoreFails(t *testing.T) {
	fixture := newHostSwitchFixture()
	fixture.failOnce = "start mdd-agent.service"
	state, err := switchHostModemServices(context.Background(), "serial", fixture, func() error { return errors.New("restore failed") }, func(context.Context) error { return nil })
	if err == nil || state != "recovery_required" || fixture.states["mdd-agent.service"].ActiveState != "inactive" {
		t.Fatal("failed rollback claimed restoration")
	}
}

func TestHostModemSwitchRollbackPreservesActiveDisabledManager(t *testing.T) {
	fixture := newHostSwitchFixture()
	mm := fixture.states["ModemManager.service"]
	mm.UnitFileState = "disabled"
	fixture.states["ModemManager.service"] = mm
	fixture.failOnce = "start mdd-agent.service"
	state, err := switchHostModemServices(context.Background(), "serial", fixture, func() error { return nil }, func(context.Context) error { return nil })
	if err == nil || state != "rolled_back" || fixture.states["ModemManager.service"].ActiveState != "active" || fixture.states["ModemManager.service"].UnitFileState != "disabled" {
		t.Fatalf("active disabled manager not restored: %s %v %+v", state, err, fixture.states)
	}
}

func TestHostModemSettingsAcceptEmptyLegacySerialProfiles(t *testing.T) {
	for _, profiles := range [][]hostModemProfile{nil, {}} {
		if err := validateHostModemSettings(hostModemSettings{Backend: "serial", Profiles: profiles}); err != nil {
			t.Fatal(err)
		}
	}
}
