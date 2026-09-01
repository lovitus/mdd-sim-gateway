package agentlink

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdaptedModemRouteFollowsCardAcrossPhysicalEquipment(t *testing.T) {
	server := modemCardTestServer(t)
	server.agents["agent-a"] = modemCardConnection("agent-a", "process-a", modemCardFact(
		"attachment-a", "862547055201716", "8985200000000000001", true, true, true))
	target, err := server.ResolveModemTargetForCardAction("8985200000000000001", ModemCallDial)
	if err != nil || target.AgentID != "agent-a" || target.EquipmentID != "862547055201716" ||
		target.AttachmentID != "attachment-a" {
		t.Fatalf("first target=%+v err=%v", target, err)
	}
	delete(server.agents, "agent-a")
	server.agents["agent-b"] = modemCardConnection("agent-b", "process-b", modemCardFact(
		"attachment-b", "867530900000002", "8985200000000000001", true, true, true))
	target, err = server.ResolveModemTargetForCardAction("8985200000000000001", ModemCallDial)
	if err != nil || target.AgentID != "agent-b" || target.ProcessGeneration != "process-b" ||
		target.EquipmentID != "867530900000002" || target.AttachmentID != "attachment-b" {
		t.Fatalf("moved target=%+v err=%v", target, err)
	}
}

func TestAdaptedModemRouteRejectsDuplicateCardAcrossModemAndReader(t *testing.T) {
	server := modemCardTestServer(t)
	server.agents["modem-agent"] = modemCardConnection("modem-agent", "modem-process", modemCardFact(
		"modem-attachment", "862547055201716", "8985200000000000001", true, true, true))
	reader := &serverConnection{
		hello:       Hello{SchemaVersion: SchemaVersion, AgentID: "reader-agent", ProcessGeneration: "reader-process"},
		connectedAt: time.Now(), lastReport: time.Now(), topology: &TopologySnapshot{
			ReaderCondition: ReaderReady,
			Readers: []ReaderFact{{ReaderName: "reader", CardPresent: true, SessionGeneration: "reader-session",
				CardID: "8985200000000000001", IdentityState: CardIdentified}},
		},
	}
	server.agents["reader-agent"] = reader
	if _, err := server.ResolveModemTargetForCardAction("8985200000000000001", ModemSMSSend); !errors.Is(err, ErrModemAmbiguous) {
		t.Fatalf("reader duplicate error=%v", err)
	}
	delete(server.agents, "reader-agent")
	server.agents["second-modem"] = modemCardConnection("second-modem", "second-process", modemCardFact(
		"second-attachment", "867530900000002", "8985200000000000001", true, true, true))
	if _, err := server.ResolveModemTargetForCardAction("8985200000000000001", ModemCallDial); !errors.Is(err, ErrModemAmbiguous) {
		t.Fatalf("modem duplicate error=%v", err)
	}
}

func TestAdaptedModemRouteCountsEnabledMultiSEProfileAsDuplicatePhysicalCard(t *testing.T) {
	server := modemCardTestServer(t)
	cardID := "8985200000000000001"
	server.agents["modem-agent"] = modemCardConnection("modem-agent", "modem-process", modemCardFact(
		"modem-attachment", "862547055201716", cardID, true, true, true))
	reader := &serverConnection{
		hello:       Hello{SchemaVersion: SchemaVersion, AgentID: "reader-agent", ProcessGeneration: "reader-process"},
		connectedAt: time.Now(), lastReport: time.Now(), topology: &TopologySnapshot{
			ReaderCondition: ReaderReady,
			Readers: []ReaderFact{{
				ReaderName: "multi-se", CardPresent: true, SessionGeneration: "reader-session",
				IdentityState: CardIdentified,
				SecureElements: []EUICCSlotFact{{SlotID: "se0", EUICC: EUICCFact{
					EID: "89049032000000000000000000000001", ProfilesAvailable: true,
					Profiles: []EUICCProfileFact{{ICCID: cardID, State: EUICCProfileEnabled}},
				}}},
			}},
		},
	}
	server.agents["reader-agent"] = reader
	if _, err := server.ResolveModemTargetForCardAction(cardID, ModemCallDial); !errors.Is(err, ErrModemAmbiguous) {
		t.Fatalf("multi-SE duplicate error=%v", err)
	}
}

func TestAdaptedModemCapabilitiesRemainIndependent(t *testing.T) {
	server := modemCardTestServer(t)
	fact := modemCardFact("attachment", "862547055201716", "8985200000000000001", true, false, true)
	server.agents["agent"] = modemCardConnection("agent", "process", fact)
	if _, err := server.ResolveModemTargetForCardAction(fact.SIM.ICCID, ModemCallDial); err != nil {
		t.Fatalf("voice route error=%v", err)
	}
	if _, err := server.ResolveModemTargetForCardAction(fact.SIM.ICCID, ModemSMSSend); !errors.Is(err, ErrModemOffline) {
		t.Fatalf("SMS route error=%v", err)
	}
	if _, err := server.ResolveModemDataTargetForCard(fact.SIM.ICCID); err != nil {
		t.Fatalf("data route error=%v", err)
	}
	fact.AT.CallSignalling = false
	fact.AT.SMS = true
	fact.Capabilities.CellularData = false
	server.agents["agent"] = modemCardConnection("agent", "process", fact)
	if _, err := server.ResolveModemTargetForCardAction(fact.SIM.ICCID, ModemCallDial); !errors.Is(err, ErrModemOffline) {
		t.Fatalf("voice route incorrectly inherited SMS: %v", err)
	}
	if _, err := server.ResolveModemTargetForCardAction(fact.SIM.ICCID, ModemSMSSend); err != nil {
		t.Fatalf("SMS route error=%v", err)
	}
	if _, err := server.ResolveModemDataTargetForCard(fact.SIM.ICCID); !errors.Is(err, ErrModemOffline) {
		t.Fatalf("data route incorrectly inherited SMS: %v", err)
	}
}

func TestCardConstrainedModemRouteNeverFallsBack(t *testing.T) {
	server := modemCardTestServer(t)
	if err := server.SetModemRouteAdmission(fixedModemAdmission{required: "importer-agent"}); err != nil {
		t.Fatal(err)
	}
	server.agents["other-agent"] = modemCardConnection("other-agent", "other-process", modemCardFact(
		"other-attachment", "862547055201716", "8985200000000000001", true, true, true))
	if _, err := server.ResolveModemTargetForCardAction("8985200000000000001", ModemCallDial); !errors.Is(err, ErrModemOffline) {
		t.Fatalf("constrained route fell back: %v", err)
	}
}

func modemCardTestServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func modemCardConnection(agentID, process string, fact ModemFact) *serverConnection {
	now := time.Now().UTC()
	return &serverConnection{
		hello:       Hello{SchemaVersion: SchemaVersion, AgentID: agentID, ProcessGeneration: process},
		connectedAt: now, lastReport: now,
		topology: &TopologySnapshot{ModemCondition: ModemReady, Modems: []ModemFact{fact}},
	}
}

func modemCardFact(attachment, equipment, card string, voice, sms, data bool) ModemFact {
	return ModemFact{
		AttachmentID: attachment, EquipmentID: equipment, Condition: "ready",
		Capabilities: ModemCapabilities{CellularData: data},
		AT:           ModemATControlFact{State: "ready", CallSignalling: voice, SMS: sms},
		SIM:          ModemSIMFact{State: "ready", SessionGeneration: "sim-" + attachment, ICCID: card},
		Network:      ModemNetworkFact{DataGuard: "protected"},
	}
}
