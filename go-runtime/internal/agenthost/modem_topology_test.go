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
		LastContinuityIssue: "sim_pin_state_failed",
		Capabilities:        agentmodem.Capabilities{CellularData: true, SMSReceive: true},
		AT:                  agentmodem.ATControlFact{State: agentmodem.ATControlReady, Port: "COM16", SIMAPDUOnDemand: true},
		SIM:                 agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "8944100000000000001", IMSI: "234100000000001", MSISDNs: []string{"+441"}},
		Network: agentmodem.NetworkFact{
			Registration: agentmodem.RegistrationRoaming, SignalPercent: &signal,
			SoftwareRadio: agentmodem.RadioOn, HardwareRadio: agentmodem.RadioOn, Data: agentmodem.DataConnected,
			Guard: agentmodem.DataGuardFact{State: agentmodem.DataGuardProtected},
		},
	}}})
	condition, detail, modems := state.snapshot()
	if condition != agentlink.ModemReady || detail != "" || len(modems) != 1 ||
		modems[0].AttachmentID != "mbn-interface" || modems[0].SIM.ICCID != "8944100000000000001" ||
		modems[0].SIM.SessionGeneration == "" || modems[0].AT.SIMAPDU || !modems[0].AT.SIMAPDUOnDemand ||
		modems[0].Network.DataGuard != "protected" || modems[0].Network.DataGuardDetail != "" ||
		modems[0].LastContinuityIssue != "sim_pin_state_failed" {
		t.Fatalf("snapshot=%s %q %+v", condition, detail, modems)
	}
	modems[0].SIM.MSISDNs[0] = "+44999"
	*modems[0].Network.SignalPercent = 1
	_, _, again := state.snapshot()
	if again[0].SIM.MSISDNs[0] != "+441" || *again[0].Network.SignalPercent != 85 {
		t.Fatal("modem topology returned mutable state storage")
	}
}

func TestModemSIMGenerationIsStableOnlyForOneLiveExactAttachment(t *testing.T) {
	state := &modemTopologyState{}
	fact := agentmodem.Fact{
		AttachmentID: "mbn-interface", EquipmentID: "862547055201716", Condition: agentmodem.DeviceReady,
		AT:  agentmodem.ATControlFact{State: agentmodem.ATControlReady, Port: "COM16", SIMAPDU: false},
		SIM: agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "8944100000000000001"},
		Network: agentmodem.NetworkFact{Registration: agentmodem.RegistrationHome,
			SoftwareRadio: agentmodem.RadioOn, HardwareRadio: agentmodem.RadioOn, Data: agentmodem.DataDisconnected},
	}
	state.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{fact}})
	_, _, first := state.snapshot()
	state.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{fact}})
	_, _, second := state.snapshot()
	if first[0].SIM.SessionGeneration == "" || first[0].SIM.SessionGeneration != second[0].SIM.SessionGeneration {
		t.Fatalf("first=%+v second=%+v", first[0].SIM, second[0].SIM)
	}
	state.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady})
	state.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{fact}})
	_, _, reinserted := state.snapshot()
	if reinserted[0].SIM.SessionGeneration == first[0].SIM.SessionGeneration {
		t.Fatal("reinserted modem SIM reused its old generation")
	}
	fact.SIM.ICCID = "8944100000000000002"
	state.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{fact}})
	_, _, replaced := state.snapshot()
	if replaced[0].SIM.SessionGeneration == reinserted[0].SIM.SessionGeneration {
		t.Fatal("replacement ICCID reused the previous generation")
	}
}

func TestModemTopologyPreservesPlatformSIMGeneration(t *testing.T) {
	state := &modemTopologyState{}
	fact := agentmodem.Fact{
		AttachmentID: "attachment", EquipmentID: "equipment", Condition: agentmodem.DeviceReady,
		SIM: agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "card", SessionGeneration: "platform-generation"},
	}
	state.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{fact}})
	_, _, modems := state.snapshot()
	if len(modems) != 1 || modems[0].SIM.SessionGeneration != "platform-generation" {
		t.Fatalf("platform generation was replaced: %+v", modems)
	}
	fact.SIM.SessionGeneration = "platform-generation-2"
	state.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{fact}})
	_, _, modems = state.snapshot()
	if modems[0].SIM.SessionGeneration != "platform-generation-2" {
		t.Fatalf("platform continuity change was not preserved: %+v", modems)
	}
}

func TestModemTopologyDoesNotInventGenerationForPlatformUnknown(t *testing.T) {
	state := &modemTopologyState{}
	state.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{{
		AttachmentID: "attachment", EquipmentID: "equipment", SessionGenerationAuthority: true,
		SIM: agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "card"},
	}}})
	_, _, modems := state.snapshot()
	if len(modems) != 1 || modems[0].SIM.SessionGeneration != "" {
		t.Fatalf("platform unknown received host fallback generation: %+v", modems)
	}
}
