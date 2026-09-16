package vowifiipc

const PeerPCSCFChanged = "peer_pcscf_changed"

// RequiresIdleRecovery includes authenticated IMS rebind requests without
// pretending that their still-live tunnel has failed.
func (snapshot Snapshot) RequiresIdleRecovery() bool {
	if snapshot.Runtime.Condition == RuntimeFailed {
		return true
	}
	return snapshot.Runtime.Condition == RuntimeRunning && (snapshot.Tunnel.Condition == LayerDegraded ||
		snapshot.IMS.Condition == LayerBlocked && snapshot.IMS.Code == PeerPCSCFChanged)
}
