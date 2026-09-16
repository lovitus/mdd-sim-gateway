package vowifiipc

import "testing"

func TestIMSRestorationRecoveryIsExplicitAndDoesNotRequireTunnelFailure(t *testing.T) {
	status := Snapshot{Runtime: RuntimeStatus{Condition: RuntimeRunning}, Tunnel: LayerStatus{Condition: LayerReady, Available: true}, IMS: LayerStatus{Condition: LayerBlocked, Code: PeerPCSCFChanged}}
	if !status.RequiresIdleRecovery() {
		t.Fatal("peer restoration omitted")
	}
	status.IMS.Code = "ims_not_registered"
	if status.RequiresIdleRecovery() {
		t.Fatal("ordinary IMS registration failure triggers full tunnel reset")
	}
	status.Runtime.Condition = RuntimeStopped
	status.IMS.Code = PeerPCSCFChanged
	if status.RequiresIdleRecovery() {
		t.Fatal("stopped runtime needs another stop")
	}
}
