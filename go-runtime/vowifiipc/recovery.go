package vowifiipc

const PeerPCSCFChanged = "peer_pcscf_changed"

// RequiresIdleRecovery identifies unhealthy sessions. It does not authorize a
// stop: Core still owns the continuous-failure budget, intent, identity and
// call/maintenance guards; the Provider rechecks the same facts at dispatch.
func (snapshot Snapshot) RequiresIdleRecovery() bool {
	if snapshot.Runtime.Condition == RuntimeFailed {
		return true
	}
	if snapshot.Runtime.Condition != RuntimeRunning {
		return false
	}
	if snapshot.Tunnel.Condition == LayerDegraded {
		return true
	}
	if snapshot.IMS.Condition != LayerBlocked {
		return false
	}
	switch snapshot.IMS.Code {
	case PeerPCSCFChanged:
		return true
	case "ims_recovery_failed", "ims_expired":
		return snapshot.Runtime.Health == nil || !snapshot.Runtime.Health.IMSRegistered
	default:
		return false
	}
}
