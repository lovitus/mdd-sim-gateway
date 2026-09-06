package agenthost

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"sync"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type modemTopologyState struct {
	mu          sync.RWMutex
	observation agentmodem.Observation
	salt        [32]byte
	counter     uint64
	sessions    map[string]modemSIMSession
}

type modemSIMSession struct {
	equipmentID string
	cardID      string
	generation  string
}

func newModemTopologyState() (*modemTopologyState, error) {
	state := &modemTopologyState{sessions: make(map[string]modemSIMSession)}
	if _, err := rand.Read(state.salt[:]); err != nil {
		return nil, err
	}
	return state, nil
}

func (state *modemTopologyState) observe(observation agentmodem.Observation) {
	copy := observation
	copy.Modems = cloneModemFacts(observation.Modems)
	state.mu.Lock()
	if state.sessions == nil {
		state.sessions = make(map[string]modemSIMSession)
	}
	nextSessions := make(map[string]modemSIMSession)
	if copy.Condition == agentmodem.ConditionReady {
		for index := range copy.Modems {
			modem := &copy.Modems[index]
			// The insertion generation establishes exact card ownership for
			// admission and fenced SIM requests. Low-level APDU support is an
			// independent capability and must not suppress call/SMS/data ownership.
			if modem.SIM.State != agentmodem.SIMReady || modem.SIM.ICCID == "" {
				modem.SIM.SessionGeneration = ""
				continue
			}
			platformGeneration := modem.SIM.SessionGeneration
			current, exists := state.sessions[modem.AttachmentID]
			if platformGeneration != "" {
				current = modemSIMSession{
					equipmentID: modem.EquipmentID, cardID: modem.SIM.ICCID, generation: platformGeneration,
				}
			} else if modem.SessionGenerationAuthority {
				continue
			} else if !exists || current.equipmentID != modem.EquipmentID || current.cardID != modem.SIM.ICCID {
				current = modemSIMSession{
					equipmentID: modem.EquipmentID, cardID: modem.SIM.ICCID,
					generation: state.nextGeneration(modem.AttachmentID, modem.EquipmentID, modem.SIM.ICCID),
				}
			}
			modem.SIM.SessionGeneration = current.generation
			nextSessions[modem.AttachmentID] = current
		}
	}
	state.sessions = nextSessions
	state.observation = copy
	state.mu.Unlock()
}

func (state *modemTopologyState) nextGeneration(attachmentID, equipmentID, cardID string) string {
	state.counter++
	hash := sha256.New()
	_, _ = hash.Write(state.salt[:])
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], state.counter)
	_, _ = hash.Write(counter[:])
	_, _ = hash.Write([]byte(attachmentID + "\x00" + equipmentID + "\x00" + cardID))
	return hex.EncodeToString(hash.Sum(nil))
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
			LastContinuityIssue: modem.LastContinuityIssue,
			Manufacturer:        modem.Manufacturer, Model: modem.Model, Firmware: modem.Firmware,
			Condition: string(modem.Condition), Detail: modem.Detail,
			Capabilities: agentlink.ModemCapabilities{
				CellularData: modem.Capabilities.CellularData, SMSReceive: modem.Capabilities.SMSReceive,
				SMSSend: modem.Capabilities.SMSSend, MBNVoiceClass: modem.Capabilities.MBNVoiceClass,
			},
			AT: agentlink.ModemATControlFact{
				State: string(modem.AT.State), Port: modem.AT.Port, Detail: modem.AT.Detail,
				CallSignalling: modem.AT.CallSignalling, SMS: modem.AT.SMS, SIMAPDU: modem.AT.SIMAPDU,
				SIMAPDUOnDemand: modem.AT.SIMAPDUOnDemand,
			},
			SIM: agentlink.ModemSIMFact{
				State: string(modem.SIM.State), SessionGeneration: modem.SIM.SessionGeneration,
				ICCID: modem.SIM.ICCID, IMSI: modem.SIM.IMSI,
				MSISDNs:  append([]string(nil), modem.SIM.MSISDNs...),
				PINState: modem.SIM.PINState, PINConfigured: modem.SIM.PINConfigured,
				PINAttempts: cloneSignal(modem.SIM.PINAttempts), PINRecovery: modem.SIM.PINRecovery,
				Configured: modem.SIM.Configured,
				SMSC:       modem.SIM.SMSC, SMSError: modem.SIM.SMSError,
			},
			Network: agentlink.ModemNetworkFact{
				Registration: string(modem.Network.Registration), OperatorID: modem.Network.OperatorID,
				OperatorName: modem.Network.OperatorName, SignalPercent: cloneSignal(modem.Network.SignalPercent),
				SoftwareRadio: string(modem.Network.SoftwareRadio), HardwareRadio: string(modem.Network.HardwareRadio),
				Data: string(modem.Network.Data), Profile: modem.Network.Profile,
				DataGuard: string(modem.Network.Guard.State), DataGuardDetail: modem.Network.Guard.Detail,
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
		result[index].SIM.PINAttempts = cloneSignal(source[index].SIM.PINAttempts)
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
