package runtimereconcile

import (
	"errors"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

func clearedExitPolicy(current recovery.ExitLedger) recovery.ExitLedger {
	return recovery.ExitLedger{LastFailureID: current.LastFailureID, LastSequence: current.LastSequence,
		SampleGeneration: current.SampleGeneration, LastManualRetryID: current.LastManualRetryID}
}

func (reconciler *Reconciler) resetExitPolicyForManualRetry(lineID, operationID string) error {
	config := reconciler.exitRecovery
	if config == nil {
		return nil
	}
	current, err := config.Store.ExitRecovery(lineID)
	if err != nil {
		return err
	}
	if current.Ledger.Selection.Pending() {
		return errors.New("exit selection is unresolved")
	}
	if current.Ledger.LastManualRetryID == operationID {
		return nil
	}
	ledger := clearedExitPolicy(current.Ledger)
	ledger.LastManualRetryID = operationID
	_, err = config.Store.PutExitRecoveryExpected(lineID, ledger, current.Revision)
	return err
}

func (reconciler *Reconciler) exitPolicyPaused(line linecatalog.Line, observation lineObservation) bool {
	config := reconciler.exitRecovery
	if config == nil {
		return false
	}
	current, err := config.Store.ExitRecovery(line.ID)
	if err != nil || current.Ledger.Selection.Pending() {
		return true
	}
	ledger := current.Ledger
	if !ledger.GivenUp && (!ledger.Exhausted || (!ledger.RetryAfter.IsZero() && !reconciler.now().Before(ledger.RetryAfter))) {
		return false
	}
	exits, err := config.Config.Snapshot()
	if err != nil {
		return true
	}
	expected := recoveryDigest([]any{recoveryDigest(line), exits.Revision, observation.status.ProviderID, observation.status.ProcessGeneration})
	if ledger.CampaignEpoch == expected {
		return true
	}
	// A saved-but-unapplied policy must not unfreeze the old runtime route.
	desired, err := egressdesired.Read(config.DesiredPath)
	if err != nil || desired.EgressConfigRevision != exits.Revision {
		return true
	}
	actual, err := egressstatus.Load(config.StatusPath)
	if err != nil || actual.DesiredGeneration != desired.Generation {
		return true
	}
	_, err = config.Store.PutExitRecoveryExpected(line.ID, clearedExitPolicy(ledger), current.Revision)
	return err != nil
}
