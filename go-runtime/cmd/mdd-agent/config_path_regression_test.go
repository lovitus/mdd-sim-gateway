package main

import (
	"path/filepath"
	"testing"
)

func TestAgentSetupPathSelection(t *testing.T) {
	t.Setenv("MDD_AGENT_CONFIG", "invalid-relative-path")
	path := filepath.Join(t.TempDir(), "config.json")
	for _, flag := range []string{"-config", "--config"} {
		actual, args, err := extractConfigPath([]string{"show", flag, path})
		if err != nil || actual != path || len(args) != 1 || args[0] != "show" {
			t.Fatalf("EXPLICIT_CONFIG_PRECEDENCE: path=%q err=%v", actual, err)
		}
	}
}
