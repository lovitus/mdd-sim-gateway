package runtimereconcile

import (
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
	"testing"
	"time"
)

func TestContinuousFailureWindowUsesElapsedTimeAndResetsOnIntent(t *testing.T) {
	now := time.Unix(1000, 0)
	r := &Reconciler{now: func() time.Time { return now }, lines: map[string]*lineState{}, continuousRetry: func() (recovery.ContinuousRetry, error) { return recovery.DefaultContinuousRetry(), nil }}
	line := linecatalog.Line{ID: "line-1"}
	observation := lineObservation{intentEpoch: 1}
	if r.continuousFailureDue(line, observation) {
		t.Fatal("first sample exhausted budget")
	}
	for range 100 {
		if r.continuousFailureDue(line, observation) {
			t.Fatal("sample count exhausted time budget")
		}
	}
	now = now.Add(119 * time.Second)
	if r.continuousFailureDue(line, observation) {
		t.Fatal("recovered early")
	}
	now = now.Add(time.Second)
	if !r.continuousFailureDue(line, observation) {
		t.Fatal("continuous failure budget never expires")
	}
	observation.intentEpoch++
	if r.continuousFailureDue(line, observation) {
		t.Fatal("new intent inherited expired window")
	}
	now = now.Add(120 * time.Second)
	r.continuousRetry = func() (recovery.ContinuousRetry, error) { return recovery.ContinuousRetry{}, errors.New("unavailable") }
	if r.continuousFailureDue(line, observation) {
		t.Fatal("unavailable settings authorized recovery")
	}
	line.Retry = &recovery.ContinuousRetry{Max: 1, Interval: 5}
	if r.continuousFailureDue(line, observation) {
		t.Fatal("line override inherited unknown window")
	}
	now = now.Add(5 * time.Second)
	if !r.continuousFailureDue(line, observation) {
		t.Fatal("per-line override was ignored")
	}
}
