package rawmodem

import (
	"errors"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func TestRawAdmissionRequiresPairedTransportAndFailClosedImportedModem(t *testing.T) {
	now := time.Date(2026, 8, 31, 4, 5, 6, 0, time.UTC)
	catalog, _, binding := rawTestCatalog()
	admission, err := NewAdmission(catalog, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	statuses := rawTestStatuses(now, binding)
	if _, constrained, err := admission.RequiredModemAgent(binding.EquipmentID, binding.CardID, statuses); !constrained || !errors.Is(err, agentlink.ErrModemOffline) {
		t.Fatalf("unpaired route constrained=%t err=%v", constrained, err)
	}
	current := &session{
		sourceAgentID: binding.SourceAgentID, sourceProcessGeneration: "source-process",
		importerAgentID: binding.ImporterAgentID, importerProcessGeneration: "importer-process",
		attachmentID: "source-attachment", sessionGeneration: "source-sim-generation",
		equipmentID: binding.EquipmentID, cardID: binding.CardID, usbSessionID: "usb-session",
		captureGeneration: "capture-generation",
	}
	statuses[0].Topology.RawUSBSessions = []agentlink.RawUSBSessionFact{rawSessionFact(current, agentlink.RawUSBExporter)}
	statuses[1].Topology.RawUSBSessions = []agentlink.RawUSBSessionFact{rawSessionFact(current, agentlink.RawUSBImporter)}
	statuses[1].Topology.ModemCondition = agentlink.ModemReady
	statuses[1].Topology.Modems = []agentlink.ModemFact{{
		AttachmentID: "imported-attachment", EquipmentID: binding.EquipmentID, Condition: "ready",
		AT:      agentlink.ModemATControlFact{State: "ready", CallSignalling: true, SMS: true},
		SIM:     agentlink.ModemSIMFact{State: "ready", ICCID: binding.CardID, SessionGeneration: "imported-sim-generation"},
		Network: agentlink.ModemNetworkFact{Data: "disconnected", DataGuard: "protected"},
	}}
	agentID, constrained, err := admission.RequiredModemAgent(binding.EquipmentID, binding.CardID, statuses)
	if err != nil || !constrained || agentID != binding.ImporterAgentID {
		t.Fatalf("agent=%q constrained=%t err=%v", agentID, constrained, err)
	}
	statuses[0].Topology.RawUSBRecoveries[0].CaptureGeneration = "stale-capture-generation"
	if _, constrained, err := admission.RequiredModemAgent(binding.EquipmentID, binding.CardID, statuses); !constrained || !errors.Is(err, agentlink.ErrModemOffline) {
		t.Fatalf("stale source capture constrained=%t err=%v", constrained, err)
	}
	statuses[0].Topology.RawUSBRecoveries[0].CaptureGeneration = "capture-generation"
	statuses[1].Topology.RawUSBSessions[0].CaptureGeneration = "stale-capture-generation"
	if _, constrained, err := admission.RequiredModemAgent(binding.EquipmentID, binding.CardID, statuses); !constrained || !errors.Is(err, agentlink.ErrModemOffline) {
		t.Fatalf("stale capture constrained=%t err=%v", constrained, err)
	}
	statuses[1].Topology.RawUSBSessions[0].CaptureGeneration = "capture-generation"
	statuses[1].Topology.Modems[0].Network.Data = "connected"
	if _, constrained, err := admission.RequiredModemAgent(binding.EquipmentID, binding.CardID, statuses); !constrained || !errors.Is(err, agentlink.ErrModemOffline) {
		t.Fatalf("connected importer constrained=%t err=%v", constrained, err)
	}
}

func TestRawAdmissionLeavesUnboundModemResolutionUnconstrained(t *testing.T) {
	now := time.Now().UTC()
	catalog, _, _ := rawTestCatalog()
	admission, err := NewAdmission(catalog, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if agentID, constrained, err := admission.RequiredModemAgent("867530900000099", "8944100000000000099", nil); err != nil || constrained || agentID != "" {
		t.Fatalf("agent=%q constrained=%t err=%v", agentID, constrained, err)
	}
}

func TestRawAdmissionIgnoresBindingWhoseLineIdentityWasReplaced(t *testing.T) {
	now := time.Now().UTC()
	catalog, line, binding := rawTestCatalog()
	catalog.snapshot.Lines[0].CardID = "8944100000000000002"
	catalog.snapshot.Lines[0].SIM.IMEI = "867530900000002"
	admission, err := NewAdmission(catalog, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if agentID, constrained, err := admission.RequiredCardAgent(binding.CardID, rawTestStatuses(now, binding)); err != nil || constrained || agentID != "" {
		t.Fatalf("line=%+v agent=%q constrained=%t err=%v", line, agentID, constrained, err)
	}
}
