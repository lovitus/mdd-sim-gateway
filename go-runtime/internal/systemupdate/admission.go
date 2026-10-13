package systemupdate

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

var ErrMaintenance = errors.New("system update maintenance is active")
var callCleanupPath = regexp.MustCompile(`^/v1/lines/[^/]+/(vowifi/calls/(end|incoming/reject)|cellular/calls/(hangup|reject))$`)

// Admission belongs to one Core update owner. Tokens cover operations already
// past their HTTP entry and background writers without holding a lock over I/O.
type Admission struct {
	mu     sync.Mutex
	lease  string
	active int
}

func OpenAdmission(store *Store) (*Admission, error) {
	record, err := store.Maintenance()
	if err != nil {
		return nil, err
	}
	gate := &Admission{}
	if record != nil && record.Phase != MaintenanceReleased {
		gate.lease = record.LeaseID
	}
	return gate, nil
}

func (gate *Admission) Enter() (func(), error) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.lease != "" {
		return nil, ErrMaintenance
	}
	gate.active++
	var once sync.Once
	return func() { once.Do(func() { gate.mu.Lock(); gate.active--; gate.mu.Unlock() }) }, nil
}

func (gate *Admission) freeze(lease string) error {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if lease == "" || gate.lease != "" && gate.lease != lease || gate.active != 0 {
		return errors.New("system update admission is busy")
	}
	gate.lease = lease
	return nil
}

func (gate *Admission) release(lease string) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.lease == lease {
		gate.lease = ""
	}
}

func (gate *Admission) Blocked() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.lease != ""
}

func (gate *Admission) owns(lease string) bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return lease != "" && gate.lease == lease
}

func (gate *Admission) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Existing streams retain their owners; new media ownership is created by
		// protected mutations. Authentication and read-only status remain usable.
		mutation := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		cleanup := r.Method == http.MethodPost && callCleanupPath.MatchString(r.URL.Path) ||
			r.Method == http.MethodDelete && (r.URL.Path == "/v1/media/leases" || r.URL.Path == "/v1/cellular/media/leases")
		if !mutation || strings.HasPrefix(r.URL.Path, "/api/auth/") || cleanup {
			next.ServeHTTP(w, r)
			return
		}
		done, err := gate.Enter()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"system_update_maintenance"}`))
			return
		}
		defer done()
		next.ServeHTTP(w, r)
	})
}
