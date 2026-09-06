package operations

import (
	"strings"
	"time"
)

const SMSCObservationTTL = 30 * time.Second

const (
	CellularSMSReady                = "cellular_sms_ready"
	CellularSMSDesiredSMSCMissing   = "cellular_sms_smsc_desired_missing"
	CellularSMSSMSCReadbackFailed   = "cellular_sms_smsc_readback_failed"
	CellularSMSSMSCReadbackMissing  = "cellular_sms_smsc_readback_missing"
	CellularSMSSMSCMismatch         = "cellular_sms_smsc_mismatch"
	CellularSMSSMSCObservationStale = "cellular_sms_smsc_observation_stale"
)

// CellularSMSCAdmission compares the durable desired SMSC with the fresh
// modem observation used for the exact paid-send route.
func CellularSMSCAdmission(desired, observed, observedError string, configured bool,
	observedAt, now time.Time,
) (bool, string) {
	desired, observed = strings.TrimSpace(desired), strings.TrimSpace(observed)
	switch {
	case desired == "":
		return false, CellularSMSDesiredSMSCMissing
	case observedAt.IsZero() || now.Before(observedAt) || now.Sub(observedAt) > SMSCObservationTTL:
		return false, CellularSMSSMSCObservationStale
	case strings.TrimSpace(observedError) != "":
		return false, CellularSMSSMSCReadbackFailed
	case !configured || observed == "":
		return false, CellularSMSSMSCReadbackMissing
	case observed != desired:
		return false, CellularSMSSMSCMismatch
	default:
		return true, CellularSMSReady
	}
}
