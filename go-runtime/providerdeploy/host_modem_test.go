package providerdeploy

import (
	"context"
	"testing"
)

func TestHostModemCommandsRejectUnrelatedUnitsAndActionsBeforeExecution(t *testing.T) {
	manager := Systemctl{Path: "/path-that-must-not-run"}
	for _, unit := range []string{"ssh.service", "mdd-core.service", "mdd-agent.service --all", "../mdd-agent.service"} {
		if err := manager.HostModemAction(context.Background(), "stop", unit); err == nil || err.Error() != "invalid host modem service" {
			t.Fatal("unexpected service admitted", unit, err)
		}
	}
	for _, action := range []string{"restart", "mask", "kill", "daemon-reload"} {
		if err := manager.HostModemAction(context.Background(), action, "mdd-agent.service"); err == nil || err.Error() != "invalid host modem service action" {
			t.Fatal("unexpected action admitted", action, err)
		}
	}
	if _, err := checkedUnit("ModemManager.service"); err == nil {
		t.Fatal("provider allowlist was widened")
	}
}

func TestHostServiceStatePreservesMaskedAndMissingStates(t *testing.T) {
	state, err := parseHostServiceState("LoadState=masked\nActiveState=inactive\nUnitFileState=masked\nMainPID=0\n")
	if err != nil || state.UnitFileState != "masked" || state.LoadState != "masked" {
		t.Fatal("service state flattened", err)
	}
	for _, input := range []string{"", "LoadState=loaded\nActiveState=active\nMainPID=unknown\n", "LoadState=loaded\nActiveState=active\nMainPID=1\nMainPID=2\n"} {
		if _, err := parseHostServiceState(input); err == nil {
			t.Fatal("invalid service snapshot accepted")
		}
	}
}
