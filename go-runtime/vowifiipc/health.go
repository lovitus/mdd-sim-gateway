package vowifiipc

import (
	"errors"
	"fmt"
	"time"
)

// RuntimeHealth contains observations, never connection credentials, subscriber
// identities or raw SIP error text. It is optional for older Providers.
type RuntimeHealth struct {
	LastInboundAt      *time.Time `json:"last_inbound_at,omitempty"`
	LastDPDSuccessAt   *time.Time `json:"last_dpd_success_at,omitempty"`
	DPDEnabled         bool       `json:"dpd_enabled"`
	DPDDead            bool       `json:"dpd_dead"`
	MissedDPDProbes    int        `json:"missed_dpd_probes"`
	IMSRegistered      bool       `json:"ims_registered"`
	IMSExpiresAt       *time.Time `json:"ims_expires_at,omitempty"`
	IMSStatusCode      int        `json:"ims_status_code"`
	IMSFailureCode     string     `json:"ims_failure_code,omitempty"`
	IMSFailures        int        `json:"ims_failures"`
	IMSRecovering      bool       `json:"ims_recovering"`
	IMSNextAttemptAt   *time.Time `json:"ims_next_attempt_at,omitempty"`
	IMSRetryAfterUntil *time.Time `json:"ims_retry_after_until,omitempty"`
}

func (health *RuntimeHealth) Validate() error {
	if health == nil {
		return nil
	}
	if health.MissedDPDProbes < 0 || health.IMSFailures < 0 || health.IMSStatusCode < 0 || health.IMSStatusCode > 699 || !validCode(health.IMSFailureCode) {
		return errors.New("invalid runtime health observation")
	}
	for _, at := range []*time.Time{health.LastInboundAt, health.LastDPDSuccessAt, health.IMSExpiresAt, health.IMSNextAttemptAt, health.IMSRetryAfterUntil} {
		if at != nil && (at.IsZero() || at.Year() < 1970 || at.Year() > 9999) {
			return errors.New("invalid runtime health time")
		}
	}
	return nil
}

// DetailFields shares one Provider observation with the existing fact projector.
// Stable, bounded scalar fields avoid publishing raw carrier error strings.
func (health *RuntimeHealth) DetailFields() []string {
	if health == nil {
		return nil
	}
	fields := []string{fmt.Sprintf("dpd_enabled=%t", health.DPDEnabled), fmt.Sprintf("dpd_dead=%t", health.DPDDead),
		fmt.Sprintf("dpd_missed=%d", health.MissedDPDProbes), fmt.Sprintf("ims_registered=%t", health.IMSRegistered),
		fmt.Sprintf("ims_status=%d", health.IMSStatusCode), fmt.Sprintf("ims_failures=%d", health.IMSFailures),
		fmt.Sprintf("ims_recovering=%t", health.IMSRecovering)}
	if health.IMSFailureCode != "" {
		fields = append(fields, "ims_failure="+health.IMSFailureCode)
	}
	for _, entry := range []struct {
		key string
		at  *time.Time
	}{
		{"last_inbound_at", health.LastInboundAt}, {"last_dpd_success_at", health.LastDPDSuccessAt},
		{"ims_expires_at", health.IMSExpiresAt}, {"ims_next_attempt_at", health.IMSNextAttemptAt}, {"ims_retry_after_until", health.IMSRetryAfterUntil}} {
		if entry.at != nil {
			fields = append(fields, entry.key+"="+entry.at.UTC().Format(time.RFC3339Nano))
		}
	}
	return fields
}

// Routine IMS backoff is not a carrier prohibition. Only an in-flight owner
// or an actual Retry-After prohibits Core's otherwise-budgeted idle recovery.
func (snapshot Snapshot) IdleRecoveryBlockedAt(now time.Time) bool {
	health := snapshot.Runtime.Health
	return health != nil && (health.IMSRecovering || health.IMSRetryAfterUntil != nil && now.Before(*health.IMSRetryAfterUntil))
}
