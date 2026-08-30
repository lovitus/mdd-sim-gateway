//go:build gui && (darwin || windows)

package main

import (
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

func TestGUISummaryUsesTypedServiceAndRuntimeState(t *testing.T) {
	value := map[string]any{
		"service": map[string]any{"state": "running"},
		"runtime": agentcontrol.Snapshot{State: agentcontrol.StateRunning},
		"topology": agentlink.TopologySnapshot{
			ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{}, ModemCondition: agentlink.ModemDisabled,
		},
	}
	if got := guiSummary(value); got != "服务：running    运行时：running    PC/SC：ready    Modem：disabled" {
		t.Fatalf("summary=%q", got)
	}
}

func TestGUISummaryDoesNotInventRuntimeHealth(t *testing.T) {
	value := map[string]any{"runtime_error": "connection refused"}
	if got := guiSummary(value); got != "运行时：unavailable    PC/SC：unavailable    Modem：unavailable" {
		t.Fatalf("summary=%q", got)
	}
}

func TestGUIBuildDefaultsToGUICommand(t *testing.T) {
	if got := defaultLaunchCommand(); got != "gui" {
		t.Fatalf("default launch command=%q", got)
	}
}
