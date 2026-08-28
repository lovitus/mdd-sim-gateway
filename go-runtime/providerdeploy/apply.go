package providerdeploy

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
)

type MaintenanceCoordinator interface {
	Request(context.Context, providerapply.DrainRequest, bool) (providerapply.DrainResult, error)
	Snapshot(context.Context) (providerapply.Snapshot, error)
}

type ApplyInput struct {
	CurrentLink      string
	PreviousTarget   string
	CandidateTarget  string
	ReceiptDirectory string
	Candidate        providerconfig.Manifest
	Current          providerconfig.Manifest
	Plan             providerapply.Plan
	Preflight        providerapply.Snapshot
	Manager          ServiceManager
	Maintenance      MaintenanceCoordinator
}

type unitState struct {
	unit    string
	active  bool
	enabled bool
}

func Execute(ctx context.Context, input ApplyInput) (Receipt, error) {
	if input.Manager == nil || input.Maintenance == nil || input.Plan.SchemaVersion != 1 || !input.Plan.Safe ||
		input.Plan.CatalogRevision == 0 || input.Candidate.CatalogRevision != input.Plan.CatalogRevision ||
		input.Preflight.CatalogRevision != input.Plan.CatalogRevision || !cleanAbsolute(input.CurrentLink) ||
		!cleanAbsolute(input.CandidateTarget) || !cleanAbsolute(input.ReceiptDirectory) ||
		(input.PreviousTarget != "" && !cleanAbsolute(input.PreviousTarget)) {
		return Receipt{}, errors.New("invalid provider apply execution input")
	}
	lock, err := AcquireLock(filepath.Join(input.ReceiptDirectory, ".apply.lock"))
	if err != nil {
		return Receipt{}, err
	}
	defer lock.Close()
	actualPrevious, err := CurrentTarget(input.CurrentLink)
	if err != nil || actualPrevious != input.PreviousTarget {
		return Receipt{}, errors.New("provider current target changed before apply")
	}
	if actualPrevious == "" {
		if input.Current.SchemaVersion != 0 || input.Current.CatalogRevision != 0 || len(input.Current.Providers) != 0 {
			return Receipt{}, errors.New("provider current manifest does not match an absent target")
		}
	} else {
		loadedCurrent, loadErr := providerconfig.LoadDirectory(actualPrevious)
		if loadErr != nil || !reflect.DeepEqual(loadedCurrent, input.Current) {
			return Receipt{}, errors.New("provider current target changed before apply")
		}
	}
	loadedCandidate, err := providerconfig.LoadDirectory(input.CandidateTarget)
	if err != nil || !reflect.DeepEqual(loadedCandidate, input.Candidate) {
		return Receipt{}, errors.New("provider candidate target changed before apply")
	}
	if !reflect.DeepEqual(providerapply.BuildPlan(input.Current, input.Candidate, input.Preflight), input.Plan) {
		return Receipt{}, errors.New("provider apply plan does not match current evidence")
	}
	return executeLocked(ctx, input)
}

func cleanAbsolute(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func executeLocked(ctx context.Context, input ApplyInput) (Receipt, error) {
	leaseID, err := newApplyID()
	if err != nil {
		return Receipt{}, err
	}
	journal, err := OpenJournal(input.ReceiptDirectory, input.PreviousTarget, input.CandidateTarget, leaseID, input.Plan)
	if err != nil {
		return Receipt{}, err
	}
	states, err := inspectUnits(ctx, input)
	if err != nil {
		return finish(journal, StateRolledBack, "unit_preflight_failed", err)
	}
	changedOrRemoved := activeChangedAndRemoved(input.Plan, states)
	drained := make(map[string]bool, len(changedOrRemoved))
	for _, lineID := range changedOrRemoved {
		drained[lineID] = true
	}
	if len(changedOrRemoved) != 0 {
		request := providerapply.DrainRequest{
			SchemaVersion: 1, CatalogRevision: input.Plan.CatalogRevision,
			LeaseID: leaseID, LineIDs: changedOrRemoved,
		}
		if err := runStep(journal, "drain", strings.Join(changedOrRemoved, ","), func() error {
			result, err := input.Maintenance.Request(ctx, request, true)
			if err != nil && hasRollbackFailure(result) {
				return errManualRecovery
			}
			return err
		}); err != nil {
			state := StateRolledBack
			if errors.Is(err, errManualRecovery) {
				state = StateManualRecovery
			}
			return finish(journal, state, errorCode(err), err)
		}
	}
	started, enabledAdded, disabledRemoved, switched := []string{}, []string{}, []string{}, false
	rollback := func(cause error) (Receipt, error) {
		rollbackErr := rollbackBeforeCommit(ctx, input, journal, states, started, enabledAdded, disabledRemoved, switched, leaseID)
		if rollbackErr != nil {
			return finish(journal, StateManualRecovery, "rollback_failed", errors.Join(cause, rollbackErr))
		}
		return finish(journal, StateRolledBack, errorCode(cause), cause)
	}
	for _, change := range append(append([]providerapply.Change{}, input.Plan.Changed...), input.Plan.Removed...) {
		state := states[change.LineID]
		if state.active {
			if err := runStep(journal, "stop", state.unit, func() error { return input.Manager.Stop(ctx, state.unit) }); err != nil {
				return rollback(err)
			}
		}
	}
	for _, change := range input.Plan.Removed {
		state := states[change.LineID]
		if state.enabled {
			if err := runStep(journal, "disable", state.unit, func() error { return input.Manager.Disable(ctx, state.unit) }); err != nil {
				return rollback(err)
			}
			disabledRemoved = append(disabledRemoved, change.LineID)
		}
	}
	if err := runStep(journal, "switch_link", input.CandidateTarget, func() error {
		return SwitchLink(input.CurrentLink, input.CandidateTarget)
	}); err != nil {
		return rollback(err)
	}
	switched = true
	for _, change := range input.Plan.Added {
		state := states[change.LineID]
		if err := runStep(journal, "enable", state.unit, func() error { return input.Manager.Enable(ctx, state.unit) }); err != nil {
			return rollback(err)
		}
		enabledAdded = append(enabledAdded, change.LineID)
	}
	for _, change := range append(append([]providerapply.Change{}, input.Plan.Changed...), input.Plan.Added...) {
		unit := unitFor(change.UnitInstance)
		if err := runStep(journal, "start", unit, func() error { return input.Manager.Start(ctx, unit) }); err != nil {
			return rollback(err)
		}
		started = append(started, change.LineID)
		active, err := input.Manager.IsActive(ctx, unit)
		if err != nil || !active {
			return rollback(errors.New("started provider is not active"))
		}
	}
	if err := waitForCandidate(ctx, input, leaseID, drained); err != nil {
		return rollback(err)
	}
	// Configuration/process replacement is committed before any drain is
	// released. A partial resume must not roll back a line already reopened.
	resumeErr := resumeCandidate(ctx, input, journal, leaseID, drained)
	if resumeErr != nil {
		return finish(journal, StateAppliedResumeIncomplete, errorCode(resumeErr), resumeErr)
	}
	if err := journal.Finish(StateApplied, "applied"); err != nil {
		return journal.Receipt(), err
	}
	return journal.Receipt(), nil
}

var errManualRecovery = errors.New("provider drain rollback failed")

func inspectUnits(ctx context.Context, input ApplyInput) (map[string]unitState, error) {
	statuses := make(map[string]providerapply.LineStatus, len(input.Preflight.Lines))
	for _, status := range input.Preflight.Lines {
		statuses[status.LineID] = status
	}
	states := make(map[string]unitState)
	for _, change := range append(append(append([]providerapply.Change{}, input.Plan.Added...), input.Plan.Changed...), input.Plan.Removed...) {
		unit := unitFor(change.UnitInstance)
		active, err := input.Manager.IsActive(ctx, unit)
		if err != nil {
			return nil, err
		}
		enabled, err := input.Manager.IsEnabled(ctx, unit)
		if err != nil {
			return nil, err
		}
		status := statuses[change.LineID]
		switch {
		case containsChange(input.Plan.Added, change.LineID) && (active || enabled || status.ProviderPresent):
			return nil, errors.New("added provider already has host state")
		case !containsChange(input.Plan.Added, change.LineID) && status.ProviderPresent != active:
			return nil, errors.New("provider registration and systemd state disagree")
		}
		states[change.LineID] = unitState{unit: unit, active: active, enabled: enabled}
	}
	return states, nil
}

func waitForCandidate(ctx context.Context, input ApplyInput, leaseID string, drained map[string]bool) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		snapshot, err := input.Maintenance.Snapshot(ctx)
		if err == nil && snapshot.CatalogRevision == input.Plan.CatalogRevision {
			statuses := make(map[string]providerapply.LineStatus, len(snapshot.Lines))
			for _, status := range snapshot.Lines {
				statuses[status.LineID] = status
			}
			ready := true
			for _, change := range append(append([]providerapply.Change{}, input.Plan.Changed...), input.Plan.Added...) {
				status := statuses[change.LineID]
				if status.Code != "provider_reachable" {
					ready = false
					break
				}
				if drained[change.LineID] &&
					(!status.Maintenance.Draining || status.Maintenance.LeaseID != leaseID) {
					return errors.New("changed provider did not retain apply drain")
				}
			}
			if ready {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("candidate providers did not register before deadline")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func resumeCandidate(ctx context.Context, input ApplyInput, journal *Journal, applyLease string, drained map[string]bool) error {
	snapshot, err := input.Maintenance.Snapshot(ctx)
	if err != nil || snapshot.CatalogRevision != input.Plan.CatalogRevision {
		return errors.New("candidate status unavailable before resume")
	}
	statuses := make(map[string]providerapply.LineStatus, len(snapshot.Lines))
	for _, status := range snapshot.Lines {
		statuses[status.LineID] = status
	}
	for _, change := range append(append([]providerapply.Change{}, input.Plan.Changed...), input.Plan.Added...) {
		status := statuses[change.LineID]
		if !status.Maintenance.Draining {
			continue
		}
		leaseID := status.Maintenance.LeaseID
		if drained[change.LineID] && leaseID != applyLease {
			return errors.New("changed provider maintenance lease changed")
		}
		request := providerapply.DrainRequest{
			SchemaVersion: 1, CatalogRevision: input.Plan.CatalogRevision,
			LeaseID: leaseID, LineIDs: []string{change.LineID},
		}
		if err := runStep(journal, "resume", change.LineID, func() error {
			_, err := input.Maintenance.Request(ctx, request, false)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func rollbackBeforeCommit(ctx context.Context, input ApplyInput, journal *Journal, states map[string]unitState,
	started, enabledAdded, disabledRemoved []string, switched bool, leaseID string,
) error {
	var failures []error
	for index := len(started) - 1; index >= 0; index-- {
		lineID := started[index]
		unit := unitFor(candidateEntry(input.Candidate, lineID).UnitInstance)
		failures = appendStepFailure(failures, runStep(journal, "rollback_stop", unit, func() error { return input.Manager.Stop(ctx, unit) }))
	}
	for index := len(enabledAdded) - 1; index >= 0; index-- {
		lineID := enabledAdded[index]
		unit := unitFor(candidateEntry(input.Candidate, lineID).UnitInstance)
		failures = appendStepFailure(failures, runStep(journal, "rollback_disable", unit, func() error { return input.Manager.Disable(ctx, unit) }))
	}
	if switched {
		target := input.PreviousTarget
		if target == "" {
			target = "<absent>"
		}
		err := runStep(journal, "rollback_link", target, func() error {
			if input.PreviousTarget == "" {
				return RemoveLink(input.CurrentLink)
			}
			return SwitchLink(input.CurrentLink, input.PreviousTarget)
		})
		failures = appendStepFailure(failures, err)
	}
	for _, lineID := range disabledRemoved {
		state := states[lineID]
		failures = appendStepFailure(failures, runStep(journal, "rollback_enable", state.unit, func() error { return input.Manager.Enable(ctx, state.unit) }))
	}
	var restartLines []string
	for _, change := range append(append([]providerapply.Change{}, input.Plan.Changed...), input.Plan.Removed...) {
		state := states[change.LineID]
		if state.active {
			failures = appendStepFailure(failures, runStep(journal, "rollback_start", state.unit, func() error { return input.Manager.Start(ctx, state.unit) }))
			restartLines = append(restartLines, change.LineID)
		}
	}
	if len(restartLines) != 0 && len(failures) == 0 {
		request := providerapply.DrainRequest{SchemaVersion: 1, CatalogRevision: input.Plan.CatalogRevision, LeaseID: leaseID, LineIDs: restartLines}
		deadline := time.Now().Add(15 * time.Second)
		for {
			_, err := input.Maintenance.Request(ctx, request, false)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				failures = append(failures, err)
				break
			}
			select {
			case <-ctx.Done():
				failures = append(failures, ctx.Err())
				return errors.Join(failures...)
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return errors.Join(failures...)
}

func runStep(journal *Journal, action, target string, run func() error) error {
	index, err := journal.Before(action, target)
	if err != nil {
		return err
	}
	if err := run(); err != nil {
		return errors.Join(err, journal.Fail(index, errorCode(err)))
	}
	return journal.Complete(index)
}

func finish(journal *Journal, state ReceiptState, code string, cause error) (Receipt, error) {
	finishErr := journal.Finish(state, code)
	return journal.Receipt(), errors.Join(cause, finishErr)
}

func errorCode(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, errManualRecovery) {
		return "manual_recovery_required"
	}
	var command *CommandError
	if errors.As(err, &command) {
		return fmt.Sprintf("systemctl_exit_%d", command.ExitCode)
	}
	return "apply_step_failed"
}

func hasRollbackFailure(result providerapply.DrainResult) bool {
	for _, line := range result.Lines {
		if line.Code == "drain_rollback_failed" {
			return true
		}
	}
	return false
}

func activeChangedAndRemoved(plan providerapply.Plan, states map[string]unitState) []string {
	result := make([]string, 0, len(plan.Changed)+len(plan.Removed))
	for _, change := range append(append([]providerapply.Change{}, plan.Changed...), plan.Removed...) {
		if states[change.LineID].active {
			result = append(result, change.LineID)
		}
	}
	sort.Strings(result)
	return result
}

func containsChange(changes []providerapply.Change, lineID string) bool {
	for _, change := range changes {
		if change.LineID == lineID {
			return true
		}
	}
	return false
}

func candidateEntry(manifest providerconfig.Manifest, lineID string) providerconfig.ManifestEntry {
	for _, entry := range manifest.Providers {
		if entry.LineID == lineID {
			return entry
		}
	}
	return providerconfig.ManifestEntry{UnitInstance: providerconfig.UnitInstance(lineID)}
}

func unitFor(instance string) string { return "mdd-vowifi@" + instance + ".service" }

func appendStepFailure(failures []error, err error) []error {
	if err != nil {
		return append(failures, err)
	}
	return failures
}
