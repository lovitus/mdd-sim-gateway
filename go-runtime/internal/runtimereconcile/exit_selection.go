package runtimereconcile

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func (reconciler *Reconciler) exitSelectionPending(lineID string) bool {
	if reconciler.exitRecovery == nil || reconciler.exitRecovery.Apply == nil {
		return false
	}
	current, err := reconciler.exitRecovery.Store.ExitRecovery(lineID)
	return err != nil || current.Ledger.Selection.Pending()
}

// The normal reconcile timer drives one shared apply worker. It never creates
// another request identity on retry, and stops automatically after five tries.
func (reconciler *Reconciler) scheduleExitSelection(catalog linecatalog.Snapshot) {
	config := reconciler.exitRecovery
	if config == nil || config.Apply == nil || reconciler.ctx.Err() != nil || !reconciler.selectionMu.TryLock() {
		return
	}
	for _, line := range catalog.Lines {
		current, err := config.Store.ExitRecovery(line.ID)
		if err != nil {
			continue
		}
		selection := current.Ledger.Selection
		if !selection.Pending() || selection.Attempts >= 5 || reconciler.now().Before(selection.NextAttempt) {
			continue
		}
		reconciler.wg.Add(1)
		go func(lineID string) {
			defer reconciler.wg.Done()
			defer reconciler.selectionMu.Unlock()
			defer reconciler.Wake()
			if err := reconciler.executeExitSelection(lineID); err != nil && reconciler.ctx.Err() == nil {
				reconciler.logf("runtime recovery selection %s: receipt persistence failed", lineID)
			}
		}(line.ID)
		return
	}
	reconciler.selectionMu.Unlock()
}

func selectionDelay(attempt int) time.Duration {
	return [...]time.Duration{30, 60, 120, 180, 240}[min(max(attempt, 1), 5)-1] * time.Second
}

func (reconciler *Reconciler) executeExitSelection(lineID string) error {
	return reconciler.runExitSelection(lineID, false)
}

func (reconciler *Reconciler) runExitSelection(lineID string, manual bool) error {
	config := reconciler.exitRecovery
	current, err := config.Store.ExitRecovery(lineID)
	if err != nil {
		return err
	}
	selection := current.Ledger.Selection
	if !selection.Pending() || (!manual && (selection.Attempts >= 5 || reconciler.now().Before(selection.NextAttempt))) {
		return nil
	}
	intent, found, _, intentErr := reconciler.catalog.RuntimeIntent(lineID)
	line, lineErr := reconciler.catalog.Get(lineID)
	if selection.Attempts == 0 && (intentErr != nil || lineErr != nil) {
		return errors.New("runtime intent unavailable")
	}
	if selection.Attempts == 0 && (!found || !intent || !line.Enabled || current.Ledger.StableCardKey != recoveryDigest(line.CardID)) {
		copy := *selection
		copy.State, copy.Code = "canceled", "runtime_intent_changed"
		current.Ledger.Selection = &copy
		_, err := config.Store.PutExitRecoveryExpected(lineID, current.Ledger, current.Revision)
		return err
	}
	copy := *selection
	copy.Attempts++
	copy.State, copy.Code = "unknown", "egress_recovery_response_pending"
	copy.NextAttempt = reconciler.now().Add(75*time.Second + selectionDelay(copy.Attempts))
	current.Ledger.Selection = &copy
	claimed, err := config.Store.PutExitRecoveryExpected(lineID, current.Ledger, current.Revision)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(reconciler.ctx, 75*time.Second)
	result, applyErr := config.Apply.RecoverEgress(ctx, copy.Request)
	cancel()
	copy.Code = "egress_recovery_unconfirmed"
	if applyErr == nil {
		actual, readErr := egressstatus.Load(config.StatusPath)
		if readErr == nil && result.SchemaVersion == egressconfig.SchemaVersion && result.ConfigRevision == copy.Request.ConfigRevision &&
			result.CatalogRevision == copy.Request.CatalogRevision && (result.State == "applied" || result.State == "unchanged") &&
			result.Code == "runtime_confirmed" && len(result.Generation) == 64 && strings.TrimLeft(result.Generation, "0123456789abcdef") == "" &&
			actual.DesiredGeneration == result.Generation && actual.Exits[copy.Request.Country].Ready && actual.Exits[copy.Request.Country].Node == copy.Request.ToNode {
			copy.State, copy.Code = "applied", "runtime_confirmed"
		}
	} else {
		var failure *egressconfig.ApplyError
		if errors.As(applyErr, &failure) {
			copy.Code = failure.Code
			if selection.Attempts == 0 && recoveryRejectedBeforePublication(failure.Code) {
				copy.State = "canceled"
			}
		}
	}
	copy.NextAttempt = time.Time{}
	if copy.Pending() && copy.Attempts < 5 {
		copy.NextAttempt = reconciler.now().Add(selectionDelay(copy.Attempts))
	}
	claimed.Ledger.Selection = &copy
	if copy.Pending() && copy.Attempts >= 5 && lineErr == nil && !copy.AttentionRecorded {
		copy.AttentionRecorded = true
		claimed.Ledger.Notice = exitNotice(line, recoveryDigest([]any{copy.Request.FailureID, "unknown"}),
			"节点切换结果仍未确认，已停止自动重试并保留恢复保护。请在维护页面核对遗留租约和实际节点。", reconciler.now())
	}
	_, err = config.Store.PutExitRecoveryExpected(lineID, claimed.Ledger, claimed.Revision)
	return err
}

func recoveryRejectedBeforePublication(code string) bool {
	switch code {
	case "invalid_egress_recovery_request", "egress_apply_revision_changed", "egress_recovery_policy_changed",
		"egress_recovery_line_changed", "egress_recovery_candidate_changed", "egress_recovery_failure_changed", "egress_recovery_healthy_peer":
		return true
	default:
		return false
	}
}
