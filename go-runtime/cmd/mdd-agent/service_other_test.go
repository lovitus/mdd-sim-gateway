//go:build !windows

package main

import (
	"reflect"
	"testing"
)

func TestManagedRestartCommandsTargetOnlyAgent(t *testing.T) {
	for _, test := range []struct {
		platform, command string
		uid               int
		args              []string
	}{
		{"linux", "systemctl", 0, []string{"--no-block", "restart", "mdd-agent.service"}},
		{"darwin", "/bin/launchctl", 501, []string{"kickstart", "-k", "gui/501/com.mdd.agent"}},
	} {
		command, args, err := managedRestartCommand(test.platform, test.uid)
		if err != nil || command != test.command || !reflect.DeepEqual(args, test.args) {
			t.Fatalf("plan: %s %v %v", command, args, err)
		}
	}
	if _, _, err := managedRestartCommand("darwin", -1); err == nil {
		t.Fatal("invalid user domain accepted")
	}
	if _, _, err := managedRestartCommand("unknown", 0); err == nil {
		t.Fatal("unknown supervisor accepted")
	}
}
