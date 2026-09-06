package operations

import (
	"testing"
	"time"
)

func TestCellularSMSCAdmission(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tests := []struct {
		name, desired, observed, observedError, code string
		configured, ready                            bool
		observedAt                                   time.Time
	}{
		{name: "exact", desired: "+441234567890", observed: "+441234567890", configured: true, observedAt: now, ready: true, code: CellularSMSReady},
		{name: "desired missing", observed: "+441234567890", configured: true, observedAt: now, code: CellularSMSDesiredSMSCMissing},
		{name: "stale", desired: "+441234567890", observed: "+441234567890", configured: true, observedAt: now.Add(-SMSCObservationTTL - time.Nanosecond), code: CellularSMSSMSCObservationStale},
		{name: "future", desired: "+441234567890", observed: "+441234567890", configured: true, observedAt: now.Add(time.Nanosecond), code: CellularSMSSMSCObservationStale},
		{name: "readback failed", desired: "+441234567890", observedError: "transport", observedAt: now, code: CellularSMSSMSCReadbackFailed},
		{name: "readback missing", desired: "+441234567890", observedAt: now, code: CellularSMSSMSCReadbackMissing},
		{name: "not configured", desired: "+441234567890", observed: "+441234567890", observedAt: now, code: CellularSMSSMSCReadbackMissing},
		{name: "mismatch", desired: "+441234567890", observed: "+449876543210", configured: true, observedAt: now, code: CellularSMSSMSCMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ready, code := CellularSMSCAdmission(test.desired, test.observed, test.observedError,
				test.configured, test.observedAt, now)
			if ready != test.ready || code != test.code {
				t.Fatalf("ready=%t code=%q", ready, code)
			}
		})
	}
}
