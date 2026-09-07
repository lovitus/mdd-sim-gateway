package notifications

import "github.com/lovitus/mdd-sim-gateway/go-runtime/internal/boltsnapshot"

// Backup returns a bounded, transactionally consistent database snapshot.
func (store *Store) Backup() ([]byte, error) {
	return boltsnapshot.Read(store.db)
}
