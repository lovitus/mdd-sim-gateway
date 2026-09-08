package runtimereconcile

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type selectionApplyFunc func(context.Context, egressconfig.RecoveryRequest) (egressconfig.ApplyResult, error)

func (call selectionApplyFunc) RecoverEgress(ctx context.Context, request egressconfig.RecoveryRequest) (egressconfig.ApplyResult, error) {
	return call(ctx, request)
}

func pendingSelectionFixture(t *testing.T, apply egressconfig.RecoveryService) (*Reconciler, linecatalog.Line) {
	t.Helper()
	r, catalog, line, observation := exitObserverFixture(t, false)
	r.exitRecovery.Apply = apply
	for i := 0; i < 3; i++ {
		observation.status.Sequence = uint64(i + 1)
		observation.status.Runtime.FailureID = strings.Repeat(string(rune('a'+i)), 64)
		if err := r.observeExitRecovery(t.Context(), catalog, line, observation); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := r.exitRecovery.Store.ExitRecovery(line.ID)
	if err != nil || !stored.Ledger.Selection.Pending() {
		t.Fatal(stored, err)
	}
	return r, line
}

func TestSelectionPersistsBeforeDispatchAndRetriesExactRequest(t *testing.T) {
	var requests []egressconfig.RecoveryRequest
	var r *Reconciler
	apply := selectionApplyFunc(func(_ context.Context, request egressconfig.RecoveryRequest) (egressconfig.ApplyResult, error) {
		requests = append(requests, request)
		stored, err := r.exitRecovery.Store.ExitRecovery(request.LineID)
		if err != nil || stored.Ledger.Selection.State != "unknown" || stored.Ledger.Selection.Attempts != len(requests) {
			t.Fatal("request was not persisted first", stored, err)
		}
		if len(requests) == 1 {
			return egressconfig.ApplyResult{}, &egressconfig.ApplyError{Status: 503, Code: "egress_apply_unavailable"}
		}
		generation := strings.Repeat("f", 64)
		payload, err := json.Marshal(egressstatus.Snapshot{DesiredGeneration: generation, Exits: map[string]egressstatus.Exit{
			request.Country: {Ready: true, Node: request.ToNode},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(r.exitRecovery.StatusPath, payload, 0600); err != nil {
			t.Fatal(err)
		}
		return egressconfig.ApplyResult{SchemaVersion: egressconfig.SchemaVersion, ConfigRevision: request.ConfigRevision, CatalogRevision: request.CatalogRevision,
			Generation: generation, State: "applied", Code: "runtime_confirmed"}, nil
	})
	var line linecatalog.Line
	r, line = pendingSelectionFixture(t, apply)
	if err := r.executeExitSelection(line.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.executeExitSelection(line.ID); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatal("backoff bypassed")
	}
	base := r.now()
	r.now = func() time.Time { return base.Add(30 * time.Second) }
	if err := r.executeExitSelection(line.ID); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatal("retry changed request identity", requests)
	}
	stored, err := r.exitRecovery.Store.ExitRecovery(line.ID)
	if err != nil || stored.Ledger.Selection.State != "applied" || stored.Ledger.Selection.Code != "runtime_confirmed" || r.exitSelectionPending(line.ID) {
		t.Fatal(stored, err)
	}
}

func TestSelectionUnknownBudgetStopsAndIntentOffCancelsUnsent(t *testing.T) {
	calls := 0
	apply := selectionApplyFunc(func(context.Context, egressconfig.RecoveryRequest) (egressconfig.ApplyResult, error) {
		calls++
		return egressconfig.ApplyResult{}, context.DeadlineExceeded
	})
	r, line := pendingSelectionFixture(t, apply)
	now := r.now()
	r.now = func() time.Time { return now }
	for i := 0; i < 6; i++ {
		if err := r.executeExitSelection(line.ID); err != nil {
			t.Fatal(err)
		}
		now = now.Add(10 * time.Minute)
	}
	stored, err := r.exitRecovery.Store.ExitRecovery(line.ID)
	if err != nil || calls != 5 || stored.Ledger.Selection.State != "unknown" || !r.exitSelectionPending(line.ID) {
		t.Fatal(calls, stored, err)
	}
	r2, line2 := pendingSelectionFixture(t, apply)
	if _, _, _, err := r2.catalog.SetRuntimeIntent(line2.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := r2.executeExitSelection(line2.ID); err != nil {
		t.Fatal(err)
	}
	canceled, err := r2.exitRecovery.Store.ExitRecovery(line2.ID)
	if err != nil || calls != 5 || canceled.Ledger.Selection.State != "canceled" {
		t.Fatal(calls, canceled, err)
	}
}

func TestSelectionRejectsClaimedSuccessWithoutTargetReadback(t *testing.T) {
	apply := selectionApplyFunc(func(_ context.Context, request egressconfig.RecoveryRequest) (egressconfig.ApplyResult, error) {
		return egressconfig.ApplyResult{SchemaVersion: egressconfig.SchemaVersion, ConfigRevision: request.ConfigRevision, CatalogRevision: request.CatalogRevision,
			Generation: request.ExpectedGeneration, State: "applied", Code: "runtime_confirmed"}, nil
	})
	r, line := pendingSelectionFixture(t, apply)
	if err := r.executeExitSelection(line.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := r.exitRecovery.Store.ExitRecovery(line.ID)
	if err != nil || stored.Ledger.Selection.State != "unknown" {
		t.Fatal("wrong node accepted as success", stored, err)
	}
	if !(&recovery.ExitSelection{State: "unknown"}).Pending() {
		t.Fatal("unknown selection lost its safety hold")
	}
}
