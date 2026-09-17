package agentmedia

// signalReservationLocked completes the reservation wait on either attachment
// or revocation. Callers hold broker.mu, including the successful attachment
// path, so signalling readiness and removal cannot close this channel twice.
// Acquire must recheck the exact record under that lock after it wakes.
func signalReservationLocked(record *reservation) {
	if record == nil || record.ready == nil {
		return
	}
	select {
	case <-record.ready:
	default:
		close(record.ready)
	}
}
