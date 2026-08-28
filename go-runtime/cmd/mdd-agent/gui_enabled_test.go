//go:build gui && (darwin || windows)

package main

import (
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

func TestGUISummaryUsesTypedServiceAndRuntimeState(t *testing.T) {
	value := map[string]any{
		"service": map[string]any{"state": "running"},
		"runtime": agentcontrol.Snapshot{State: agentcontrol.StateRunning},
	}
	if got := guiSummary(value); got != "服务：running    运行时：running" {
		t.Fatalf("summary=%q", got)
	}
}

func TestGUISummaryDoesNotInventRuntimeHealth(t *testing.T) {
	value := map[string]any{"runtime_error": "connection refused"}
	if got := guiSummary(value); got != "运行时：unavailable" {
		t.Fatalf("summary=%q", got)
	}
}
