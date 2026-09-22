package callhistory

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"time"
)

// CallObservation is a fresh presentation fact, never terminal recovery evidence.
type CallObservation struct {
	LineID     string
	CardID     string
	Generation string
	ReceivedAt time.Time
	Snapshot   vowifiipc.Snapshot
}

func (store *Store) observeCall(snapshot vowifiipc.Snapshot, card string, at time.Time) {
	store.observationMu.Lock()
	defer store.observationMu.Unlock()
	if store.observations == nil {
		store.observations = make(map[string]CallObservation)
	}
	old := store.observations[snapshot.LineID]
	if old.Generation == snapshot.ProcessGeneration && (old.Snapshot.Sequence > snapshot.Sequence || old.Snapshot.Sequence == snapshot.Sequence && !snapshot.ObservedAt.After(old.Snapshot.ObservedAt)) {
		return
	}
	if snapshot.PendingIncomingCall != nil {
		pending := *snapshot.PendingIncomingCall
		snapshot.PendingIncomingCall = &pending
	}
	if snapshot.ActiveCall != nil {
		active := *snapshot.ActiveCall
		snapshot.ActiveCall = &active
	}
	store.observations[snapshot.LineID] = CallObservation{LineID: snapshot.LineID, CardID: card, Generation: snapshot.ProcessGeneration, ReceivedAt: at, Snapshot: snapshot}
}
func (store *Store) CurrentVoWiFiCalls(now time.Time) []CallObservation {
	store.observationMu.RLock()
	defer store.observationMu.RUnlock()
	result := []CallObservation{}
	for _, row := range store.observations {
		if now.Sub(row.ReceivedAt) > 30*time.Second || now.Sub(row.Snapshot.ObservedAt) > 30*time.Second || row.ReceivedAt.After(now.Add(time.Minute)) {
			continue
		}
		copy := row
		if row.Snapshot.PendingIncomingCall != nil {
			pending := *row.Snapshot.PendingIncomingCall
			copy.Snapshot.PendingIncomingCall = &pending
		}
		if row.Snapshot.ActiveCall != nil {
			active := *row.Snapshot.ActiveCall
			copy.Snapshot.ActiveCall = &active
		}
		result = append(result, copy)
	}
	return result
}
