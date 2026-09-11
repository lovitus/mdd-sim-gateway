package agentsim

import (
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

// This is a read-only preflight, not permission to cancel a card operation.
func (manager *Manager) CheckRestartSafe() error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if len(manager.refreshExpected) != 0 {
		return errors.New("card refresh is pending")
	}
	manager.downloadMu.Lock()
	for _, job := range manager.downloads {
		state := job.snapshot().State
		if state != agentlink.EUICCDownloadCompleted && state != agentlink.EUICCDownloadFailed && state != agentlink.EUICCDownloadCanceled {
			manager.downloadMu.Unlock()
			return errors.New("eUICC download is active or uncertain")
		}
	}
	manager.downloadMu.Unlock()
	for _, session := range manager.sessions {
		if !session.operation.TryLock() {
			return errors.New("card operation is active")
		}
		session.operation.Unlock()
	}
	return nil
}
