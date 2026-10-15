//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

// The existing helper owns this single journal and its exact maintenance set.
// Restart/retry cleans the original attempt before admitting another apply.
type egressAttempt struct {
	Document        egressdesired.Document            `json:"document"`
	Previous        *egressdesired.Document           `json:"previous,omitempty"`
	PreviousRuntime egressdesired.RuntimeConfirmation `json:"previous_runtime"`
	Drain           providerapply.DrainRequest        `json:"drain"`
	Phase           string                            `json:"phase"`
	Result          egressconfig.ApplyResult          `json:"result"`
}

func (service *providerApplyService) egressAttemptPath() string {
	root := service.settings.ProviderApply.ReceiptPath
	if root == "" {
		root = filepath.Dir(service.settings.ProviderApply.EgressDesiredPath)
	}
	return filepath.Join(root, "egress-current.json")
}

func (service *providerApplyService) readEgressAttempt() (*egressAttempt, error) {
	path := service.egressAttemptPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, errors.New("egress apply journal is invalid")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var attempt egressAttempt
	if json.Unmarshal(payload, &attempt) != nil || attempt.Document.AttemptID == "" || attempt.Drain.LeaseID != attempt.Document.AttemptID {
		return nil, errors.New("egress apply journal is invalid")
	}
	switch attempt.Phase {
	case "draining", "drained", "publishing", "confirmed", "complete":
	default:
		return nil, errors.New("egress apply phase is invalid")
	}
	return &attempt, nil
}

func (service *providerApplyService) saveEgressAttempt(attempt *egressAttempt) error {
	payload, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	path := service.egressAttemptPath()
	file, err := os.CreateTemp(filepath.Dir(path), ".egress-apply-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func (service *providerApplyService) prepareEgressAttempt(ctx context.Context, address string, document egressdesired.Document, drain providerapply.DrainRequest) (*egressAttempt, error) {
	snapshot, err := providerapply.Fetch(ctx, address+providerapply.Path, service.settings.Local.Token, nil)
	if err != nil || snapshot.CatalogRevision != drain.CatalogRevision {
		return nil, errors.New("egress maintenance snapshot unavailable")
	}
	drain.LineIDs, drain.ExpectedGenerations = nil, map[string]string{}
	for _, line := range snapshot.Lines {
		if !line.ProviderPresent {
			continue
		}
		if line.Code != "provider_reachable" || line.ProcessGeneration == "" {
			return nil, errors.New("egress maintenance owner unavailable")
		}
		drain.LineIDs = append(drain.LineIDs, line.LineID)
		drain.ExpectedGenerations[line.LineID] = line.ProcessGeneration
	}
	sort.Strings(drain.LineIDs)
	attempt := &egressAttempt{Document: document, Drain: drain, Phase: "draining"}
	previous, err := egressdesired.Read(service.settings.ProviderApply.EgressDesiredPath)
	if err == nil {
		attempt.Previous = &previous
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	attempt.PreviousRuntime, _ = egressdesired.ReadRuntime(service.settings.ProviderApply.EgressStatusPath)
	return attempt, service.saveEgressAttempt(attempt)
}

func (service *providerApplyService) confirmEgressAttempt(ctx context.Context, attempt *egressAttempt) error {
	actual, err := egressdesired.WaitForAttempt(ctx, service.settings.ProviderApply.EgressStatusPath, attempt.Document, 30*time.Second)
	if err != nil {
		return egressFailure(http.StatusGatewayTimeout, "egress_runtime_confirmation_unconfirmed", err)
	}
	result := egressconfig.ApplyResult{SchemaVersion: egressconfig.SchemaVersion,
		ConfigRevision: attempt.Document.EgressConfigRevision, CatalogRevision: attempt.Document.CatalogRevision,
		Generation: attempt.Document.Generation, State: actual.Outcome, Code: "runtime_confirmed"}
	if actual.Outcome == "rolled_back" || actual.Outcome == "not_applied" {
		old := attempt.PreviousRuntime
		if old.ConfigIdentity == "" || actual.ConfigIdentity != old.ConfigIdentity || actual.DesiredGeneration != old.DesiredGeneration {
			return egressFailure(http.StatusConflict, "egress_previous_runtime_unconfirmed", nil)
		}
		result.Code = "egress_previous_runtime_restored"
	} else if actual.Outcome != "applied" && actual.Outcome != "partial" && actual.Outcome != "unavailable" {
		return egressFailure(http.StatusConflict, "egress_runtime_outcome_invalid", nil)
	}
	for country, exit := range actual.Exits {
		if !exit.Ready {
			result.FailedCountries = append(result.FailedCountries, country)
		}
	}
	sort.Strings(result.FailedCountries)
	if actual.Outcome == "unavailable" {
		result.Code = "egress_runtime_unavailable"
	}
	attempt.Result, attempt.Phase = result, "confirmed"
	return service.saveEgressAttempt(attempt)
}

func (service *providerApplyService) releaseEgressAttempt(ctx context.Context, address string, attempt *egressAttempt) error {
	if len(attempt.Drain.LineIDs) == 0 {
		attempt.Phase = "complete"
		return service.saveEgressAttempt(attempt)
	}
	snapshot, err := providerapply.Fetch(ctx, address+providerapply.Path, service.settings.Local.Token, nil)
	if err != nil {
		return err
	}
	remaining := attempt.Drain
	remaining.CatalogRevision = snapshot.CatalogRevision
	remaining.LineIDs, remaining.ExpectedGenerations = nil, map[string]string{}
	resumable := remaining
	resumable.ExpectedGenerations = map[string]string{}
	for _, id := range attempt.Drain.LineIDs {
		pending := true
		for _, line := range snapshot.Lines {
			if line.LineID != id {
				continue
			}
			if !line.ProviderPresent || line.Code != "provider_reachable" || line.ProcessGeneration != attempt.Drain.ExpectedGenerations[id] {
				break
			}
			if line.Maintenance.Draining && line.Maintenance.LeaseID == attempt.Drain.LeaseID {
				resumable.LineIDs = append(resumable.LineIDs, id)
				resumable.ExpectedGenerations[id] = line.ProcessGeneration
			} else {
				pending = false // The original lease is gone; leave other leases alone.
			}
			break
		}
		if pending {
			remaining.LineIDs = append(remaining.LineIDs, id)
			remaining.ExpectedGenerations[id] = attempt.Drain.ExpectedGenerations[id]
		}
	}
	attempt.Drain = remaining
	if err := service.saveEgressAttempt(attempt); err != nil {
		return err
	}
	if len(resumable.LineIDs) != 0 {
		result, err := providerapply.RequestMaintenance(ctx, address, service.settings.Local.Token, resumable, false, nil)
		pending := remaining.LineIDs[:0]
		for _, id := range remaining.LineIDs {
			resumed := false
			for _, line := range result.Lines {
				if line.LineID == id && line.Code == "resumed" && line.ProcessGeneration == resumable.ExpectedGenerations[id] {
					resumed = true
				}
			}
			if !resumed {
				pending = append(pending, id)
			} else {
				delete(remaining.ExpectedGenerations, id)
			}
		}
		remaining.LineIDs = pending
		attempt.Drain = remaining
		if saveErr := service.saveEgressAttempt(attempt); saveErr != nil {
			return saveErr
		}
		if err != nil || len(pending) > 0 {
			return errors.Join(errors.New("egress maintenance release unconfirmed"), err)
		}
	}
	if len(attempt.Drain.LineIDs) != 0 {
		return errors.New("original egress maintenance owners remain unconfirmed")
	}
	attempt.Phase = "complete"
	return service.saveEgressAttempt(attempt)
}

func (service *providerApplyService) recoverEgressAttempt(ctx context.Context, address string) (*egressconfig.ApplyResult, error) {
	attempt, err := service.readEgressAttempt()
	if err != nil || attempt == nil || attempt.Phase == "complete" {
		return nil, err
	}
	if attempt.Phase == "publishing" {
		current, readErr := egressdesired.Read(service.settings.ProviderApply.EgressDesiredPath)
		neverPublished := errors.Is(readErr, os.ErrNotExist) && attempt.Previous == nil ||
			readErr == nil && attempt.Previous != nil && current.AttemptID == attempt.Previous.AttemptID && current.Generation == attempt.Previous.Generation
		if !neverPublished {
			if err := service.confirmEgressAttempt(ctx, attempt); err != nil {
				return nil, err
			}
		}
	}
	if err := service.releaseEgressAttempt(ctx, address, attempt); err != nil {
		return nil, egressFailure(http.StatusConflict, "egress_maintenance_resume_unconfirmed", err)
	}
	if attempt.Result.Generation != "" {
		return &attempt.Result, nil
	}
	return nil, nil
}

// This only finishes the durable original attempt. It cannot publish a new
// configuration, acquire a new maintenance lease, or restart an exit process.
func (service *providerApplyService) resumeEgressAttempts(ctx context.Context) {
	address, err := providerCoreAddress(service.settings.Local.Listen)
	if err != nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	lastError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if service.mutation.TryLock() {
			bounded, cancel := context.WithTimeout(ctx, 35*time.Second)
			_, err = service.recoverEgressAttempt(bounded, address)
			cancel()
			service.mutation.Unlock()
			message := ""
			if err != nil {
				message = "egress maintenance recovery remains unconfirmed"
			}
			if message != lastError && message != "" {
				log.Print(message)
			}
			lastError = message
		}
		timer.Reset(30 * time.Second)
	}
}
