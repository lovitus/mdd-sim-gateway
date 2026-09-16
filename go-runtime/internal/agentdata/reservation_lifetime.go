package agentdata

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

// remove retires the exact connection whose acknowledgement failed. A late
// failure from an old handler must not revoke a same-ID replacement.
func (broker *Broker) remove(streamID string, expected *reservation) {
	broker.mu.Lock()
	if broker.items[streamID] == expected {
		delete(broker.items, streamID)
	}
	signalReservationLocked(expected)
	broker.mu.Unlock()
	if expected != nil && expected.conn != nil {
		_ = expected.conn.Close()
	}
}
