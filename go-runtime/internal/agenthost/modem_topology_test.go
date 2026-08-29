package agenthost

import (
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func TestModemTopologyMapsFactsWithoutConflatingAttachmentAndSIM(t *testing.T) {
	signal := uint32(85)
	state := &modemTopologyState{}
	state.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{{
		AttachmentID: "mbn-interface", EquipmentID: "imei", Condition: agentmodem.DeviceReady,
		Capabilities: agentmodem.Capabilities{CellularData: true, SMSReceive: true},
		SIM:          agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "8944100000000000001", IMSI: "234100000000001", MSISDNs: []string{"+441"}},
		Network: agentmodem.NetworkFact{
			Registration: agentmodem.RegistrationRoaming, SignalPercent: &signal,
			SoftwareRadio: agentmodem.RadioOn, HardwareRadio: agentmodem.RadioOn, Data: agentmodem.DataConnected,
		},
	}}})
	condition, detail, modems := state.snapshot()
	if condition != agentlink.ModemReady || detail != "" || len(modems) != 1 ||
		modems[0].AttachmentID != "mbn-interface" || modems[0].SIM.ICCID != "8944100000000000001" {
		t.Fatalf("snapshot=%s %q %+v", condition, detail, modems)
	}
	modems[0].SIM.MSISDNs[0] = "+44999"
	*modems[0].Network.SignalPercent = 1
	_, _, again := state.snapshot()
	if again[0].SIM.MSISDNs[0] != "+441" || *again[0].Network.SignalPercent != 85 {
		t.Fatal("modem topology returned mutable state storage")
	}
}
