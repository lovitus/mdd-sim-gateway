package agenthost

import (
	"sort"
	"sync"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type modemTopologyState struct {
	mu          sync.RWMutex
	observation agentmodem.Observation
}

func (state *modemTopologyState) observe(observation agentmodem.Observation) {
	copy := observation
	copy.Modems = cloneModemFacts(observation.Modems)
	state.mu.Lock()
	state.observation = copy
	state.mu.Unlock()
}

func (state *modemTopologyState) snapshot() (agentlink.ModemCondition, string, []agentlink.ModemFact) {
	state.mu.RLock()
	observation := state.observation
	observation.Modems = cloneModemFacts(observation.Modems)
	state.mu.RUnlock()
	if observation.Condition == "" {
		observation.Condition = agentmodem.ConditionStarting
	}
	modems := make([]agentlink.ModemFact, 0, len(observation.Modems))
	for _, modem := range observation.Modems {
		modems = append(modems, agentlink.ModemFact{
			AttachmentID: modem.AttachmentID, EquipmentID: modem.EquipmentID,
			Manufacturer: modem.Manufacturer, Model: modem.Model, Firmware: modem.Firmware,
			Condition: string(modem.Condition), Detail: modem.Detail,
			Capabilities: agentlink.ModemCapabilities{
				CellularData: modem.Capabilities.CellularData, SMSReceive: modem.Capabilities.SMSReceive,
				SMSSend: modem.Capabilities.SMSSend, MBNVoiceClass: modem.Capabilities.MBNVoiceClass,
			},
			AT: agentlink.ModemATControlFact{
				State: string(modem.AT.State), Port: modem.AT.Port, Detail: modem.AT.Detail,
				CallSignalling: modem.AT.CallSignalling, SMS: modem.AT.SMS, SIMAPDU: modem.AT.SIMAPDU,
			},
			SIM: agentlink.ModemSIMFact{
				State: string(modem.SIM.State), ICCID: modem.SIM.ICCID, IMSI: modem.SIM.IMSI,
				MSISDNs: append([]string(nil), modem.SIM.MSISDNs...), Configured: modem.SIM.Configured,
				SMSC: modem.SIM.SMSC, SMSError: modem.SIM.SMSError,
			},
			Network: agentlink.ModemNetworkFact{
				Registration: string(modem.Network.Registration), OperatorID: modem.Network.OperatorID,
				OperatorName: modem.Network.OperatorName, SignalPercent: cloneSignal(modem.Network.SignalPercent),
				SoftwareRadio: string(modem.Network.SoftwareRadio), HardwareRadio: string(modem.Network.HardwareRadio),
				Data: string(modem.Network.Data), Profile: modem.Network.Profile,
			},
		})
	}
	sort.Slice(modems, func(left, right int) bool { return modems[left].AttachmentID < modems[right].AttachmentID })
	return agentlink.ModemCondition(observation.Condition), observation.Detail, modems
}

func cloneModemFacts(source []agentmodem.Fact) []agentmodem.Fact {
	result := make([]agentmodem.Fact, len(source))
	copy(result, source)
	for index := range result {
		result[index].SIM.MSISDNs = append([]string(nil), source[index].SIM.MSISDNs...)
		result[index].Network.SignalPercent = cloneSignal(source[index].Network.SignalPercent)
	}
	return result
}

func cloneSignal(source *uint32) *uint32 {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}
