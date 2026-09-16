package agentsim

import (
	"context"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

type MetadataRecoveryResult struct {
	Attempt        int
	Code           string
	Recovered      bool
	ValidationRule string
}

// RepairInvalidMetadata retries only read-only metadata on a live, fenced card.
// It never reopens hardware, changes a profile, verifies PIN, or waits on busy IO.
func (manager *Manager) RepairInvalidMetadata(ctx context.Context, now time.Time) []MetadataRecoveryResult {
	var results []MetadataRecoveryResult
	for _, view := range manager.Sessions() {
		reader := agentlink.ReaderFact{ReaderName: view.ReaderName, CardPresent: true, SessionGeneration: view.SessionGeneration,
			CardID: view.CardID, SIM: view.SIM, EUICC: view.EUICC, SecureElements: view.SecureElements, IdentityState: agentlink.CardIdentified}
		err := (agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{reader}}).Validate()
		if err == nil {
			continue
		}
		validationRule := err.Error()
		manager.downloadMu.Lock()
		downloadBusy := false
		for _, job := range manager.downloads {
			if job.sessionGeneration != view.SessionGeneration {
				continue
			}
			state := job.snapshot().State
			if state != agentlink.EUICCDownloadCompleted && state != agentlink.EUICCDownloadFailed && state != agentlink.EUICCDownloadCanceled {
				downloadBusy = true
				break
			}
		}
		manager.downloadMu.Unlock()
		if downloadBusy {
			continue
		}
		// An invalid card identifier cannot authorize a hardware readback.
		if (agentlink.ReaderReadbackRequest{OperationID: "metadata-recovery", ProcessGeneration: "local",
			ReaderName: view.ReaderName, CardID: view.CardID, SIMSessionGeneration: view.SessionGeneration}).Validate() != nil {
			continue
		}
		manager.mu.RLock()
		current := manager.sessions[view.SessionGeneration]
		manager.mu.RUnlock()
		if current == nil || !current.operation.TryLock() {
			continue
		}
		if !current.active.Load() || current.ctx.Err() != nil || current.cardID != view.CardID || current.readerName != view.ReaderName ||
			current.metadataRecoveryAttempts >= 3 || now.Before(current.metadataRecoveryNext) {
			current.operation.Unlock()
			continue
		}
		current.metadataRecoveryAttempts++
		current.metadataRecoveryNext = now.Add(time.Duration(1<<current.metadataRecoveryAttempts) * 30 * time.Second)
		readContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, readErr := manager.readCardIdentityLocked(readContext, current, view.ReaderName, view.SessionGeneration, view.CardID)
		cancel()
		result := MetadataRecoveryResult{Attempt: current.metadataRecoveryAttempts, Code: readerReadbackErrorCode(readErr), Recovered: readErr == nil, ValidationRule: validationRule}
		if readErr == nil {
			result.Code = "reader_metadata_recovered"
		}
		current.operation.Unlock()
		results = append(results, result)
		break // Bound one scan to one card transaction, including on multi-reader hosts.
	}
	return results
}
