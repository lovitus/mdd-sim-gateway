package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/operations"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

func TestDeviceProjectionAdaptedModemUsesExactIdentityAndExistingReadiness(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	line := deviceTestLine("line-1", "8944100000000000001", "862547055201716")
	status := deviceModemStatus(now, "agent-a", "process-a", "attachment-a", line.SIM.IMEI, line.CardID)
	readiness := state.Readiness{Ready: true, Facts: []state.FactView{}}
	projection := events.LineProjection{LineID: line.ID, Operations: map[string]state.Readiness{
		operations.CellularCall: readiness,
	}}
	snapshot := projectDevices(now, []agentlink.ConnectionStatus{status}, linecatalog.Snapshot{
		SchemaVersion: 1, Revision: 2, Lines: []linecatalog.Line{line},
	}, []events.LineProjection{projection}, linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 1})
	if len(snapshot.Devices) != 1 || snapshot.Devices[0].Mode != "adapted" || len(snapshot.Devices[0].Endpoints) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	endpoint := snapshot.Devices[0].Endpoints[0]
	if endpoint.Association != "exact" || !endpoint.OperationCandidate || endpoint.Line == nil ||
		endpoint.Line.ID != line.ID || !endpoint.Line.Operations[operations.CellularCall].Ready ||
		endpoint.Line.Operations[operations.VoWiFiCall].Ready {
		t.Fatalf("endpoint=%+v", endpoint)
	}

	duplicate := status
	duplicate.AgentID, duplicate.ProcessGeneration = "agent-b", "process-b"
	snapshot = projectDevices(now, []agentlink.ConnectionStatus{status, duplicate}, linecatalog.Snapshot{
		SchemaVersion: 1, Revision: 2, Lines: []linecatalog.Line{line},
	}, []events.LineProjection{projection}, linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 1})
	if len(snapshot.Devices) != 2 {
		t.Fatalf("duplicate snapshot=%+v", snapshot)
	}
	for _, device := range snapshot.Devices {
		if device.Endpoints[0].Association != "ambiguous" || device.Endpoints[0].OperationCandidate {
			t.Fatalf("duplicate device=%+v", device)
		}
	}
}

func TestDeviceProjectionTreatsMultiSESlotsAsIndependentEndpoints(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	first := deviceTestLine("line-a", "8944100000000000001", "")
	second := deviceTestLine("line-b", "8944100000000000002", "")
	reader := agentlink.ReaderFact{
		ReaderName: "reader-a", CardPresent: true, SessionGeneration: "reader-session",
		CardID: first.CardID, IdentityState: agentlink.CardIdentified,
		SecureElements: []agentlink.EUICCSlotFact{
			{SlotID: "se0", Label: "SE1", EUICC: agentlink.EUICCFact{EID: "89049032000000000000000000000001", ProfilesAvailable: true,
				Profiles: []agentlink.EUICCProfileFact{{ICCID: first.CardID, State: agentlink.EUICCProfileEnabled}}}},
			{SlotID: "se1", Label: "SE2", EUICC: agentlink.EUICCFact{EID: "89049032000000000000000000000002", ProfilesAvailable: true,
				Profiles: []agentlink.EUICCProfileFact{{ICCID: second.CardID, State: agentlink.EUICCProfileEnabled}}}},
		},
	}
	status := deviceReaderStatus(now, "agent-a", "process-a", reader)
	snapshot := projectDevices(now, []agentlink.ConnectionStatus{status}, linecatalog.Snapshot{
		SchemaVersion: 1, Revision: 3, Lines: []linecatalog.Line{first, second},
	}, nil, linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 1})
	if len(snapshot.Devices) != 1 || len(snapshot.Devices[0].Endpoints) != 2 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	for _, endpoint := range snapshot.Devices[0].Endpoints {
		if endpoint.Association != "exact" || endpoint.Line == nil || !endpoint.OperationCandidate {
			t.Fatalf("endpoint=%+v", endpoint)
		}
		for _, operation := range endpoint.Line.Operations {
			if operation.Ready || len(operation.Blocked) == 0 {
				t.Fatalf("missing line projection was not fail closed: %+v", endpoint.Line)
			}
		}
	}

	reader.SecureElements[0].EUICC.Profiles = append(reader.SecureElements[0].EUICC.Profiles,
		agentlink.EUICCProfileFact{ICCID: "8944100000000000003", State: agentlink.EUICCProfileEnabled})
	status = deviceReaderStatus(now, "agent-a", "process-a", reader)
	snapshot = projectDevices(now, []agentlink.ConnectionStatus{status}, linecatalog.Snapshot{
		SchemaVersion: 1, Revision: 3, Lines: []linecatalog.Line{first, second},
	}, nil, linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 1})
	if got := snapshot.Devices[0].Endpoints[0]; got.Association != "ambiguous" || got.Code != "multiple_enabled_profiles" {
		t.Fatalf("multi-enabled endpoint=%+v", got)
	}
}

func TestDeviceProjectionReaderDisabledAndBlankProfilesDoNotAssociate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	cardID := "8944100000000000001"
	reader := agentlink.ReaderFact{
		ReaderName: "reader-a", CardPresent: true, SessionGeneration: "reader-session",
		IdentityState: agentlink.CardIdentified, SecureElements: []agentlink.EUICCSlotFact{{
			SlotID: "se0", EUICC: agentlink.EUICCFact{EID: "89049032000000000000000000000001", ProfilesAvailable: true,
				Profiles: []agentlink.EUICCProfileFact{{ICCID: cardID, State: agentlink.EUICCProfileDisabled}}},
		}},
	}
	snapshot := projectDevices(now, []agentlink.ConnectionStatus{deviceReaderStatus(now, "agent-a", "process-a", reader)},
		linecatalog.Snapshot{SchemaVersion: 1, Revision: 2, Lines: []linecatalog.Line{deviceTestLine("line-a", cardID, "")}},
		nil, linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 1})
	endpoint := snapshot.Devices[0].Endpoints[0]
	if len(endpoint.CardIDs) != 0 || endpoint.Association != "unmatched" || endpoint.OperationCandidate {
		t.Fatalf("blank endpoint=%+v", endpoint)
	}
}

func TestDeviceProjectionDuplicateDirectReadersAndSecureElementsAreAmbiguous(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	line := deviceTestLine("line-a", "8944100000000000001", "")
	reader := agentlink.ReaderFact{
		ReaderName: "reader-a", CardPresent: true, SessionGeneration: "reader-session",
		CardID: line.CardID, IdentityState: agentlink.CardIdentified,
	}
	duplicate := reader
	duplicate.ReaderName = "reader-b"
	snapshot := projectDevices(now, []agentlink.ConnectionStatus{
		deviceReaderStatus(now, "agent-a", "process-a", reader),
		deviceReaderStatus(now, "agent-b", "process-b", duplicate),
	}, linecatalog.Snapshot{SchemaVersion: 1, Revision: 2, Lines: []linecatalog.Line{line}}, nil,
		linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 1})
	if len(snapshot.Devices) != 2 {
		t.Fatalf("direct reader snapshot=%+v", snapshot)
	}
	for _, device := range snapshot.Devices {
		endpoint := device.Endpoints[0]
		if endpoint.Association != "ambiguous" || endpoint.Code != "card_identity_ambiguous" ||
			endpoint.OperationCandidate {
			t.Fatalf("duplicate direct endpoint=%+v", endpoint)
		}
	}

	reader = agentlink.ReaderFact{
		ReaderName: "reader-a", CardPresent: true, IdentityState: agentlink.CardIdentified,
		SecureElements: []agentlink.EUICCSlotFact{
			{SlotID: "se0", EUICC: agentlink.EUICCFact{EID: "89049032000000000000000000000001",
				ProfilesAvailable: true, Profiles: []agentlink.EUICCProfileFact{{ICCID: line.CardID, State: agentlink.EUICCProfileEnabled}}}},
			{SlotID: "se1", EUICC: agentlink.EUICCFact{EID: "89049032000000000000000000000002",
				ProfilesAvailable: true, Profiles: []agentlink.EUICCProfileFact{{ICCID: line.CardID, State: agentlink.EUICCProfileEnabled}}}},
		},
	}
	snapshot = projectDevices(now, []agentlink.ConnectionStatus{deviceReaderStatus(now, "agent-a", "process-a", reader)},
		linecatalog.Snapshot{SchemaVersion: 1, Revision: 2, Lines: []linecatalog.Line{line}}, nil,
		linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 1})
	if len(snapshot.Devices) != 1 || len(snapshot.Devices[0].Endpoints) != 2 {
		t.Fatalf("secure element snapshot=%+v", snapshot)
	}
	for _, endpoint := range snapshot.Devices[0].Endpoints {
		if endpoint.Association != "ambiguous" || endpoint.Code != "card_identity_ambiguous" ||
			endpoint.OperationCandidate {
			t.Fatalf("duplicate secure-element endpoint=%+v", endpoint)
		}
	}
}

func TestDeviceProjectionHealthyRawMergesSourceAndImporter(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	line := deviceTestLine("line-raw", "8944100000000000001", "862547055201716")
	binding := linecatalog.RawModemBinding{
		SchemaVersion: 1, Epoch: 2, LineID: line.ID, SourceAgentID: "source-agent",
		EquipmentID: line.SIM.IMEI, CardID: line.CardID, ImporterAgentID: "importer-agent", Enabled: true,
	}
	source, importer := deviceRawStatuses(now, binding)
	snapshot := projectDevices(now, []agentlink.ConnectionStatus{source, importer}, linecatalog.Snapshot{
		SchemaVersion: 1, Revision: 2, Lines: []linecatalog.Line{line},
	}, []events.LineProjection{{LineID: line.ID, Operations: map[string]state.Readiness{
		operations.CellularCall: {Ready: true, Facts: []state.FactView{}},
	}}}, linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 2, Bindings: []linecatalog.RawModemBinding{binding}})
	if len(snapshot.Devices) != 1 {
		t.Fatalf("healthy raw duplicated physical device: %+v", snapshot)
	}
	device := snapshot.Devices[0]
	if device.Mode != "raw" || device.Condition != "ready" || device.Raw == nil || !device.Raw.TransportReady ||
		device.Raw.ImportedModem == nil || len(device.Endpoints) != 1 || !device.Endpoints[0].OperationCandidate ||
		device.Endpoints[0].Line == nil || device.Endpoints[0].Line.ID != line.ID {
		t.Fatalf("raw device=%+v", device)
	}

	importer.Topology.RawUSBSessions = nil
	snapshot = projectDevices(now, []agentlink.ConnectionStatus{source, importer}, linecatalog.Snapshot{
		SchemaVersion: 1, Revision: 2, Lines: []linecatalog.Line{line},
	}, nil, linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 2, Bindings: []linecatalog.RawModemBinding{binding}})
	if len(snapshot.Devices) != 2 {
		t.Fatalf("degraded raw should retain source and orphan importer: %+v", snapshot)
	}
	for _, current := range snapshot.Devices {
		if current.Mode != "raw" || current.Condition == "ready" || current.Endpoints[0].OperationCandidate {
			t.Fatalf("degraded raw device=%+v", current)
		}
	}
}

func TestDeviceProjectionRawPartialAndMismatchedStatesStayInoperable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	line := deviceTestLine("line-raw", "8944100000000000001", "862547055201716")
	binding := linecatalog.RawModemBinding{
		SchemaVersion: 1, Epoch: 2, LineID: line.ID, SourceAgentID: "source-agent",
		EquipmentID: line.SIM.IMEI, CardID: line.CardID, ImporterAgentID: "importer-agent", Enabled: true,
	}
	source, importer := deviceRawStatuses(now, binding)
	catalog := linecatalog.Snapshot{SchemaVersion: 1, Revision: 2, Lines: []linecatalog.Line{line}}
	bindings := linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 2, Bindings: []linecatalog.RawModemBinding{binding}}

	unbound := projectDevices(now, []agentlink.ConnectionStatus{source}, catalog, nil,
		linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 1})
	if len(unbound.Devices) != 1 || unbound.Devices[0].Mode != "raw" ||
		unbound.Devices[0].Endpoints[0].Association != "exact" ||
		unbound.Devices[0].Endpoints[0].Code != "raw_capture_unbound" ||
		unbound.Devices[0].Endpoints[0].OperationCandidate {
		t.Fatalf("unbound source=%+v", unbound)
	}

	importerOnly := projectDevices(now, []agentlink.ConnectionStatus{importer}, catalog, nil, bindings)
	if len(importerOnly.Devices) != 1 || importerOnly.Devices[0].Mode != "raw" ||
		importerOnly.Devices[0].Condition != "degraded" || importerOnly.Devices[0].Endpoints[0].OperationCandidate {
		t.Fatalf("importer-only modem=%+v", importerOnly)
	}
	importerWithoutModem := importer
	importerTopology := agentlink.NormalizeTopology(*importer.Topology)
	importerTopology.Modems = nil
	importerWithoutModem.Topology = &importerTopology
	importerOnly = projectDevices(now, []agentlink.ConnectionStatus{importerWithoutModem}, catalog, nil, bindings)
	if len(importerOnly.Devices) != 1 || importerOnly.Devices[0].Code != "raw_imported_modem_missing" ||
		importerOnly.Devices[0].Raw == nil || len(importerOnly.Devices[0].Raw.ImporterSessions) != 1 ||
		importerOnly.Devices[0].Endpoints[0].OperationCandidate {
		t.Fatalf("importer-only session=%+v", importerOnly)
	}

	staleImporter := importer
	staleTopology := agentlink.NormalizeTopology(*importer.Topology)
	staleTopology.RawUSBSessions[0].CaptureGeneration = "stale-capture-generation"
	staleImporter.Topology = &staleTopology
	stale := projectDevices(now, []agentlink.ConnectionStatus{source, staleImporter}, catalog, nil, bindings)
	if len(stale.Devices) != 2 {
		t.Fatalf("stale capture snapshot=%+v", stale)
	}
	for _, device := range stale.Devices {
		if device.Mode != "raw" || device.Condition == "ready" || device.Endpoints[0].OperationCandidate {
			t.Fatalf("stale capture device=%+v", device)
		}
	}

	mismatchImporter := importer
	mismatchTopology := agentlink.NormalizeTopology(*importer.Topology)
	mismatchTopology.Modems[0].EquipmentID = "862547055201799"
	mismatchImporter.Topology = &mismatchTopology
	mismatch := projectDevices(now, []agentlink.ConnectionStatus{source, mismatchImporter}, catalog, nil, bindings)
	if len(mismatch.Devices) != 2 {
		t.Fatalf("identity mismatch snapshot=%+v", mismatch)
	}
	foundMismatch := false
	for _, device := range mismatch.Devices {
		if device.AgentID == binding.ImporterAgentID {
			foundMismatch = device.Mode == "raw" && device.Code == "raw_import_identity_mismatch" &&
				!device.Endpoints[0].OperationCandidate
		}
	}
	if !foundMismatch {
		t.Fatalf("imported identity mismatch was not fail closed: %+v", mismatch)
	}
}

func TestDeviceProjectionDuplicateRawSourcesAreAmbiguous(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	line := deviceTestLine("line-raw", "8944100000000000001", "862547055201716")
	binding := linecatalog.RawModemBinding{SchemaVersion: 1, Epoch: 2, LineID: line.ID, SourceAgentID: "source-agent",
		EquipmentID: line.SIM.IMEI, CardID: line.CardID, ImporterAgentID: "importer-agent", Enabled: true}
	source, _ := deviceRawStatuses(now, binding)
	duplicate := source
	duplicate.AgentID, duplicate.ProcessGeneration = "other-source", "other-process"
	duplicateTopology := agentlink.NormalizeTopology(*source.Topology)
	duplicate.Topology = &duplicateTopology
	for index := range duplicate.Topology.RawUSBRecoveries {
		duplicate.Topology.RawUSBRecoveries[index].AttachmentID = "other-attachment"
	}
	snapshot := projectDevices(now, []agentlink.ConnectionStatus{source, duplicate}, linecatalog.Snapshot{
		SchemaVersion: 1, Revision: 2, Lines: []linecatalog.Line{line},
	}, nil, linecatalog.RawModemSnapshot{SchemaVersion: 1, Revision: 2, Bindings: []linecatalog.RawModemBinding{binding}})
	if len(snapshot.Devices) != 2 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	for _, device := range snapshot.Devices {
		if device.Condition != "ambiguous" || device.Endpoints[0].Association != "ambiguous" ||
			device.Endpoints[0].OperationCandidate {
			t.Fatalf("duplicate raw=%+v", device)
		}
	}
}

func TestDevicesEndpointIsReadOnlyAndBrowserSnapshotRemainsAdditive(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "lines.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	line := deviceTestLine("line-1", "8944100000000000001", "862547055201716")
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	facts := fixedAgentFacts{statuses: []agentlink.ConnectionStatus{
		deviceModemStatus(now, "agent-a", "process-a", "attachment-a", line.SIM.IMEI, line.CardID),
	}}
	server := NewServer(testReplay(t, now), func() time.Time { return now },
		WithAgentFacts(facts), WithLineCatalog(catalog, linecatalog.NewHandler(catalog)))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/devices", nil))
	var snapshot DeviceSnapshot
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &snapshot) != nil || len(snapshot.Devices) != 1 {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/devices?guess=true", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("query status=%d", response.Code)
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/devices", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("mutation status=%d", response.Code)
	}
	legacy, err := json.Marshal(BrowserSnapshot{Type: "browser.snapshot", SchemaVersion: 1,
		Lines: []events.LineProjection{}, Agents: []agentlink.ConnectionStatus{},
		Catalog: linecatalog.Snapshot{SchemaVersion: 1, Lines: []linecatalog.Line{}}})
	if err != nil || bytes.Contains(legacy, []byte(`"devices"`)) {
		t.Fatalf("empty additive field changed legacy snapshot: %s err=%v", legacy, err)
	}
	withDevices, err := json.Marshal(BrowserSnapshot{Type: "browser.snapshot", SchemaVersion: 1,
		Lines: []events.LineProjection{}, Agents: []agentlink.ConnectionStatus{},
		Catalog: linecatalog.Snapshot{SchemaVersion: 1, Lines: []linecatalog.Line{}}, Devices: snapshot.Devices})
	if err != nil || !bytes.Contains(withDevices, []byte(`"devices"`)) {
		t.Fatalf("populated additive field missing: %s err=%v", withDevices, err)
	}
}

func deviceTestLine(id, cardID, equipmentID string) linecatalog.Line {
	return linecatalog.Line{SchemaVersion: 1, ID: id, Name: id, Enabled: true, CardID: cardID,
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: equipmentID}}
}

func deviceModemStatus(now time.Time, agentID, process, attachment, equipment, card string) agentlink.ConnectionStatus {
	return agentlink.ConnectionStatus{AgentID: agentID, ProcessGeneration: process, LastSeen: now, LastReport: now,
		Topology: &agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{},
			ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
				AttachmentID: attachment, EquipmentID: equipment, Condition: "ready",
				AT:      agentlink.ModemATControlFact{State: "ready", CallSignalling: true, SMS: true},
				SIM:     agentlink.ModemSIMFact{State: "ready", ICCID: card, SessionGeneration: "sim-session"},
				Network: agentlink.ModemNetworkFact{Data: "disconnected", DataGuard: "protected"},
			}}}}
}

func deviceReaderStatus(now time.Time, agentID, process string, reader agentlink.ReaderFact) agentlink.ConnectionStatus {
	return agentlink.ConnectionStatus{AgentID: agentID, ProcessGeneration: process, LastSeen: now, LastReport: now,
		Topology: &agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady,
			Readers: []agentlink.ReaderFact{reader}, ModemCondition: agentlink.ModemDisabled, Modems: []agentlink.ModemFact{}}}
}

func deviceRawStatuses(now time.Time, binding linecatalog.RawModemBinding) (agentlink.ConnectionStatus, agentlink.ConnectionStatus) {
	capture := agentlink.RawUSBRecoveryFact{AttachmentID: "source-attachment", SessionGeneration: "source-sim-session",
		EquipmentID: binding.EquipmentID, CardID: binding.CardID, USBSessionID: "usb-session",
		CaptureGeneration: "capture-generation", State: "capture_reserved"}
	session := agentlink.RawUSBSessionFact{SourceAgentID: binding.SourceAgentID, SourceProcessGeneration: "source-process",
		AttachmentID: capture.AttachmentID, SessionGeneration: capture.SessionGeneration,
		EquipmentID: binding.EquipmentID, CardID: binding.CardID, USBSessionID: capture.USBSessionID,
		CaptureGeneration: capture.CaptureGeneration, State: "transport_active"}
	exporter := session
	exporter.Role = agentlink.RawUSBExporter
	imported := session
	imported.Role = agentlink.RawUSBImporter
	source := agentlink.ConnectionStatus{AgentID: binding.SourceAgentID, ProcessGeneration: "source-process", LastSeen: now, LastReport: now,
		Topology: &agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{},
			ModemCondition: agentlink.ModemDisabled, Modems: []agentlink.ModemFact{}, RawUSBSource: true,
			RawUSBRecoveries: []agentlink.RawUSBRecoveryFact{capture}, RawUSBSessions: []agentlink.RawUSBSessionFact{exporter}}}
	importer := deviceModemStatus(now, binding.ImporterAgentID, "importer-process", "imported-attachment", binding.EquipmentID, binding.CardID)
	importer.Topology.RawUSBImporter = true
	importer.Topology.RawUSBSessions = []agentlink.RawUSBSessionFact{imported}
	return source, importer
}
