package runtimehost

import "time"

// Called with m.mu held; successful registration time is a fact, not a lease
// that can be extended by repeatedly reading status.
func (m *imsRegistrationMaintenance) registrationExpiredLocked(now time.Time) bool {
	if m.registeredAt.IsZero() {
		return false
	}
	expires, _, _ := imsRegistrationSchedule(m.config, m.binding, m.session, m.registeredAt, true)
	return !expires.IsZero() && !now.Before(expires)
}
