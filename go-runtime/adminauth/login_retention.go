package adminauth

import "time"

const maxLoginPeers = 4096

// Bound both retained failures and space reserved for admitted derivations.
// Do not evict a throttled peer: changing source addresses must not erase an
// existing peer's rate limit. Known peers can still authenticate at capacity.
func (manager *Manager) canTrackLoginPeerLocked(peer string) bool {
	if _, known := manager.failures[peer]; known {
		return true
	}
	reserved := len(manager.failures)
	for inFlight := range manager.loginInFlight {
		if _, known := manager.failures[inFlight]; !known {
			reserved++
		}
	}
	return reserved < maxLoginPeers
}

func (manager *Manager) sweepLoginFailuresLocked(now time.Time) {
	if !manager.lastFailureSweep.IsZero() && now.Before(manager.lastFailureSweep.Add(time.Minute)) {
		return
	}
	manager.lastFailureSweep = now
	for peer := range manager.failures {
		// retryAfterLocked removes histories with no attempts in the window.
		manager.retryAfterLocked(peer, now)
	}
}
