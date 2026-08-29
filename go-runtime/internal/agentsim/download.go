package agentsim

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

type downloadJob struct {
	mu                sync.RWMutex
	sessionGeneration string
	eid               string
	requestSHA256     string
	status            agentlink.EUICCDownloadJob
	cancel            context.CancelFunc
}

func (job *downloadJob) snapshot() agentlink.EUICCDownloadJob {
	job.mu.RLock()
	defer job.mu.RUnlock()
	return cloneDownloadJob(job.status)
}

func (job *downloadJob) update(update func(*agentlink.EUICCDownloadJob)) agentlink.EUICCDownloadJob {
	job.mu.Lock()
	defer job.mu.Unlock()
	update(&job.status)
	return cloneDownloadJob(job.status)
}

func (manager *Manager) ExecuteEUICCDownload(_ context.Context,
	request agentlink.EUICCDownloadRequest) agentlink.EUICCDownloadResponse {
	result := agentlink.EUICCDownloadResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
		EID: request.EID, Action: request.Action,
	}
	if err := request.Validate(); err != nil {
		result.Failure = failure("rejected", "invalid_euicc_download_request", false)
		return result
	}
	manager.mu.RLock()
	current := manager.sessions[request.SessionGeneration]
	manager.mu.RUnlock()
	if current == nil || !current.active.Load() {
		result.Failure = failure("not_ready", "card_session_replaced", true)
		return result
	}
	if current.euicc == nil || current.euicc.EID != request.EID || !current.euicc.ProfileDownload {
		result.Failure = failure("conflict", "euicc_identity_mismatch", false)
		return result
	}
	switch request.Action {
	case agentlink.EUICCDownloadStart:
		return manager.startEUICCDownload(current, request, result)
	case agentlink.EUICCDownloadStatus:
		return manager.statusEUICCDownload(request, result)
	case agentlink.EUICCDownloadCancel:
		return manager.cancelEUICCDownload(request, result)
	default:
		result.Failure = failure("rejected", "invalid_euicc_download_request", false)
		return result
	}
}

func (manager *Manager) startEUICCDownload(current *session, request agentlink.EUICCDownloadRequest,
	result agentlink.EUICCDownloadResponse) agentlink.EUICCDownloadResponse {
	digest := downloadRequestDigest(request)
	now := time.Now().UTC()
	manager.downloadMu.Lock()
	defer manager.downloadMu.Unlock()
	if prior := manager.downloads[request.OperationID]; prior != nil {
		if prior.requestSHA256 != "" && prior.requestSHA256 != digest {
			result.Failure = failure("conflict", "euicc_download_operation_conflict", false)
			return result
		}
		job := prior.snapshot()
		result.Job = &job
		return result
	}
	for _, active := range manager.downloads {
		status := active.snapshot()
		if active.sessionGeneration == request.SessionGeneration && !downloadTerminal(status.State) {
			result.Failure = failure("conflict", "euicc_download_already_running", true)
			return result
		}
	}
	if manager.downloadStore != nil {
		initial := agentlink.EUICCDownloadJob{
			State: agentlink.EUICCDownloadQueued, Stage: agentlink.EUICCDownloadStageQueued,
			StartedAt: now, UpdatedAt: now,
		}
		record, created, err := manager.downloadStore.Begin(DownloadRecord{
			OperationID: request.OperationID, EID: request.EID, Job: initial,
		})
		if err != nil {
			if errors.Is(err, ErrDownloadConflict) {
				result.Failure = failure("conflict", "euicc_download_operation_conflict", false)
			} else {
				result.Failure = failure("failed", "euicc_download_state_unavailable", false)
			}
			return result
		}
		if !created {
			prior := &downloadJob{
				sessionGeneration: request.SessionGeneration, eid: request.EID,
				status: cloneDownloadJob(record.Job),
			}
			manager.downloads[request.OperationID] = prior
			job := prior.snapshot()
			result.Job = &job
			return result
		}
	}
	jobContext, cancel := context.WithTimeout(current.ctx, manager.downloadTimeout)
	job := &downloadJob{
		sessionGeneration: request.SessionGeneration, eid: request.EID, requestSHA256: digest, cancel: cancel,
		status: agentlink.EUICCDownloadJob{
			State: agentlink.EUICCDownloadQueued, Stage: agentlink.EUICCDownloadStageQueued,
			StartedAt: now, UpdatedAt: now,
		},
	}
	manager.downloads[request.OperationID] = job
	jobSnapshot := job.snapshot()
	result.Job = &jobSnapshot
	go manager.runEUICCDownload(jobContext, current, request, job)
	return result
}

func (manager *Manager) statusEUICCDownload(request agentlink.EUICCDownloadRequest,
	result agentlink.EUICCDownloadResponse) agentlink.EUICCDownloadResponse {
	manager.downloadMu.Lock()
	job := manager.downloads[request.OperationID]
	manager.downloadMu.Unlock()
	if job != nil {
		if job.eid != request.EID {
			result.Failure = failure("conflict", "euicc_download_not_found", false)
			return result
		}
		status := job.snapshot()
		result.Job = &status
		return result
	}
	if manager.downloadStore == nil {
		result.Failure = failure("conflict", "euicc_download_not_found", false)
		return result
	}
	record, found, err := manager.downloadStore.Get(request.OperationID)
	if err != nil {
		result.Failure = failure("failed", "euicc_download_state_unavailable", false)
		return result
	}
	if !found || record.EID != request.EID {
		result.Failure = failure("conflict", "euicc_download_not_found", false)
		return result
	}
	status := cloneDownloadJob(record.Job)
	result.Job = &status
	return result
}

func (manager *Manager) cancelEUICCDownload(request agentlink.EUICCDownloadRequest,
	result agentlink.EUICCDownloadResponse) agentlink.EUICCDownloadResponse {
	manager.downloadMu.Lock()
	job := manager.downloads[request.OperationID]
	manager.downloadMu.Unlock()
	if job == nil {
		return manager.statusEUICCDownload(request, result)
	}
	if job.eid != request.EID {
		result.Failure = failure("conflict", "euicc_download_not_found", false)
		return result
	}
	status := job.update(func(status *agentlink.EUICCDownloadJob) {
		if downloadTerminal(status.State) {
			return
		}
		status.State = agentlink.EUICCDownloadCancelling
		status.Code = "cancel_requested"
		status.UpdatedAt = time.Now().UTC()
	})
	if !downloadTerminal(status.State) && job.cancel != nil {
		job.cancel()
	}
	result.Job = &status
	return result
}

func (manager *Manager) runEUICCDownload(ctx context.Context, current *session,
	request agentlink.EUICCDownloadRequest, job *downloadJob) {
	defer job.cancel()
	current.operation.Lock()
	defer current.operation.Unlock()
	if err := ctx.Err(); err != nil {
		state, code := agentlink.EUICCDownloadCanceled, "download_canceled"
		if errors.Is(err, context.DeadlineExceeded) {
			state, code = agentlink.EUICCDownloadFailed, "download_timeout"
		}
		manager.finishDownload(request, job, state, agentlink.EUICCDownloadStageQueued, code, nil)
		return
	}
	if !current.active.Load() {
		manager.finishDownload(request, job, agentlink.EUICCDownloadFailed,
			agentlink.EUICCDownloadStageQueued, "card_session_replaced", nil)
		return
	}
	if err := current.card.BeginTransaction(); err != nil {
		manager.finishDownload(request, job, agentlink.EUICCDownloadFailed,
			agentlink.EUICCDownloadStageQueued, "pcsc_transaction_failed", nil)
		return
	}
	transactionOpen := true
	endTransaction := func() error {
		if !transactionOpen {
			return nil
		}
		transactionOpen = false
		return current.card.EndTransaction()
	}
	live, err := inspectEUICC(ctx, current.card)
	if err != nil || live == nil || live.EID != request.EID || !live.ProfileDownload {
		_ = endTransaction()
		if contextErr := ctx.Err(); contextErr != nil {
			state, code := agentlink.EUICCDownloadCanceled, "download_canceled"
			if errors.Is(contextErr, context.DeadlineExceeded) {
				state, code = agentlink.EUICCDownloadFailed, "download_timeout"
			}
			manager.finishDownload(request, job, state, agentlink.EUICCDownloadStageQueued, code, nil)
			return
		}
		manager.finishDownload(request, job, agentlink.EUICCDownloadFailed,
			agentlink.EUICCDownloadStageQueued, "euicc_identity_changed", nil)
		return
	}
	stage := agentlink.EUICCDownloadStageAuthenticateClient
	job.update(func(status *agentlink.EUICCDownloadJob) {
		status.State, status.Stage, status.Code = agentlink.EUICCDownloadRunning, stage, ""
		status.UpdatedAt = time.Now().UTC()
	})
	err = manager.downloadProfile(ctx, current.card, request,
		func(next agentlink.EUICCDownloadStage) {
			stage = next
			job.update(func(status *agentlink.EUICCDownloadJob) {
				status.State, status.Stage, status.Code = agentlink.EUICCDownloadRunning, next, ""
				status.UpdatedAt = time.Now().UTC()
			})
		},
		func(metadata *agentlink.EUICCDownloadMetadata) {
			job.update(func(status *agentlink.EUICCDownloadJob) {
				status.Metadata = metadata
				status.UpdatedAt = time.Now().UTC()
			})
		})
	endErr := endTransaction()
	if err == nil && endErr == nil {
		current.requestRefresh()
		manager.finishDownload(request, job, agentlink.EUICCDownloadCompleted,
			agentlink.EUICCDownloadStageCompleted, "", job.snapshot().Metadata)
		return
	}
	if stage == agentlink.EUICCDownloadStageInstall {
		current.requestRefresh()
		manager.finishDownload(request, job, agentlink.EUICCDownloadUncertain, stage,
			"euicc_download_install_uncertain", job.snapshot().Metadata)
		return
	}
	state, code := agentlink.EUICCDownloadFailed, "euicc_download_failed"
	if errors.Is(ctx.Err(), context.Canceled) {
		state, code = agentlink.EUICCDownloadCanceled, "download_canceled"
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = "download_timeout"
	}
	manager.finishDownload(request, job, state, stage, code, job.snapshot().Metadata)
}

func (manager *Manager) finishDownload(request agentlink.EUICCDownloadRequest, job *downloadJob,
	state agentlink.EUICCDownloadState, stage agentlink.EUICCDownloadStage, code string,
	metadata *agentlink.EUICCDownloadMetadata) {
	status := job.update(func(status *agentlink.EUICCDownloadJob) {
		status.State, status.Stage, status.Code = state, stage, code
		status.Metadata = metadata
		status.UpdatedAt = time.Now().UTC()
	})
	if manager.downloadStore == nil {
		return
	}
	if _, err := manager.downloadStore.Mark(request.OperationID, status); err != nil {
		job.update(func(status *agentlink.EUICCDownloadJob) {
			status.State = agentlink.EUICCDownloadUncertain
			status.Code = "euicc_download_state_unavailable"
			status.UpdatedAt = time.Now().UTC()
		})
	}
}

func downloadRequestDigest(request agentlink.EUICCDownloadRequest) string {
	hash := sha256.New()
	for _, value := range []string{request.EID, request.ActivationCode, request.ConfirmationCode, request.IMEI} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func downloadTerminal(state agentlink.EUICCDownloadState) bool {
	switch state {
	case agentlink.EUICCDownloadCompleted, agentlink.EUICCDownloadFailed,
		agentlink.EUICCDownloadCanceled, agentlink.EUICCDownloadUncertain:
		return true
	default:
		return false
	}
}
