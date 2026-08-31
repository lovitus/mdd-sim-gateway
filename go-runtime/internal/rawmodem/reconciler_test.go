package rawmodem

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentusbip"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type fakeCatalog struct {
	mu       sync.Mutex
	snapshot linecatalog.Snapshot
	raw      linecatalog.RawModemSnapshot
}

func (catalog *fakeCatalog) Snapshot() (linecatalog.Snapshot, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	return catalog.snapshot, nil
}

func (catalog *fakeCatalog) Get(id string) (linecatalog.Line, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	for _, line := range catalog.snapshot.Lines {
		if line.ID == id {
			return line, nil
		}
	}
	return linecatalog.Line{}, linecatalog.ErrNotFound
}

func (catalog *fakeCatalog) RawModemBindings() (linecatalog.RawModemSnapshot, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	return catalog.raw, nil
}

func (catalog *fakeCatalog) replaceBindingEpoch() {
	catalog.mu.Lock()
	catalog.raw.Revision++
	catalog.raw.Bindings[0].Epoch++
	catalog.mu.Unlock()
}

type fakeAgents struct {
	statuses   []agentlink.ConnectionStatus
	sequence   *[]string
	requests   []agentlink.RawUSBRequest
	afterStart func(agentlink.RawUSBRequest)
	stopError  error
}

func (agents *fakeAgents) Statuses() []agentlink.ConnectionStatus { return agents.statuses }

func (agents *fakeAgents) ExecuteRawUSB(_ context.Context, _, _ string,
	request agentlink.RawUSBRequest) (agentlink.RawUSBResponse, error) {
	agents.requests = append(agents.requests, request)
	*agents.sequence = append(*agents.sequence, string(request.Action)+":"+string(request.Role))
	if request.Action == agentlink.RawUSBStop && request.Role == agentlink.RawUSBImporter && agents.stopError != nil {
		return agentlink.RawUSBResponse{}, agents.stopError
	}
	if agents.afterStart != nil && request.Action != agentlink.RawUSBStop {
		agents.afterStart(request)
	}
	response := agentlink.RawUSBResponse{}
	if request.Action == agentlink.RawUSBExportStart {
		response.Device = &agentlink.RawUSBDevice{BusID: "1-2", VendorID: 0x2c7c, ProductID: 0x0125}
	}
	return response, nil
}

type fakeBroker struct {
	sequence    *[]string
	reservation agentusbip.Reservation
	revoked     []string
}

func (broker *fakeBroker) Reserve(input agentusbip.Reservation) error {
	*broker.sequence = append(*broker.sequence, "reserve")
	broker.reservation = input
	return nil
}

func (broker *fakeBroker) Revoke(streamID string) {
	*broker.sequence = append(*broker.sequence, "revoke")
	broker.revoked = append(broker.revoked, streamID)
}

func TestRawReconcilerStartsExporterBeforeImporterWithDistinctTokens(t *testing.T) {
	now := time.Date(2026, 8, 31, 2, 3, 4, 0, time.UTC)
	catalog, line, binding := rawTestCatalog()
	sequence := []string{}
	agents := &fakeAgents{statuses: rawTestStatuses(now, binding), sequence: &sequence}
	broker := &fakeBroker{sequence: &sequence}
	reconciler := newRawTestReconciler(t, catalog, agents, broker, now)
	if err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"reserve", "export_start:exporter", "import_start:importer"}
	if len(sequence) != len(want) {
		t.Fatalf("sequence=%v", sequence)
	}
	for index := range want {
		if sequence[index] != want[index] {
			t.Fatalf("sequence=%v", sequence)
		}
	}
	if broker.reservation.ExporterStreamToken == "" || broker.reservation.ImporterStreamToken == "" ||
		broker.reservation.ExporterStreamToken == broker.reservation.ImporterStreamToken {
		t.Fatalf("role tokens were not independent: %+v", broker.reservation)
	}
	if state := reconciler.states[line.ID]; state == nil || state.session == nil || state.session.bindingKey != bindingKey(binding) {
		t.Fatalf("state=%+v", state)
	}
}

func TestRawSessionRequiresBothTransportFactsAndImportedModem(t *testing.T) {
	now := time.Now().UTC()
	_, _, binding := rawTestCatalog()
	statuses := rawTestStatuses(now, binding)
	current := &session{
		sourceAgentID: binding.SourceAgentID, sourceProcessGeneration: "source-process",
		importerAgentID: binding.ImporterAgentID, importerProcessGeneration: "importer-process",
		attachmentID: "source-attachment", sessionGeneration: "source-sim-generation",
		equipmentID: binding.EquipmentID, cardID: binding.CardID, usbSessionID: "usb-session",
	}
	for index := range statuses {
		role := agentlink.RawUSBExporter
		if statuses[index].AgentID == binding.ImporterAgentID {
			role = agentlink.RawUSBImporter
		}
		statuses[index].Topology.RawUSBSessions = []agentlink.RawUSBSessionFact{rawSessionFact(current, role)}
	}
	if sessionReady(current, statuses, now, time.Minute) {
		t.Fatal("transport facts without imported ordinary modem were accepted")
	}
	statuses[1].Topology.ModemCondition = agentlink.ModemReady
	statuses[1].Topology.Modems = []agentlink.ModemFact{{
		AttachmentID: "imported-attachment", EquipmentID: binding.EquipmentID, Condition: "ready",
		AT:      agentlink.ModemATControlFact{State: "ready"},
		SIM:     agentlink.ModemSIMFact{State: "ready", ICCID: binding.CardID, SessionGeneration: "imported-sim-generation"},
		Network: agentlink.ModemNetworkFact{Data: "disconnected", DataGuard: "protected"},
	}}
	if !sessionReady(current, statuses, now, time.Minute) {
		t.Fatal("exact importer modem plus both transport facts were not accepted")
	}
}

func TestRawBindingEpochChangeCleansPartialExporter(t *testing.T) {
	now := time.Now().UTC()
	catalog, _, binding := rawTestCatalog()
	sequence := []string{}
	agents := &fakeAgents{statuses: rawTestStatuses(now, binding), sequence: &sequence}
	agents.afterStart = func(request agentlink.RawUSBRequest) {
		if request.Action == agentlink.RawUSBExportStart {
			catalog.replaceBindingEpoch()
		}
	}
	broker := &fakeBroker{sequence: &sequence}
	reconciler := newRawTestReconciler(t, catalog, agents, broker, now)
	err := reconciler.reconcile(context.Background())
	if err == nil {
		t.Fatal("binding epoch change was not rejected")
	}
	want := []string{"reserve", "export_start:exporter", "stop:exporter", "revoke"}
	if len(sequence) != len(want) {
		t.Fatalf("sequence=%v err=%v", sequence, err)
	}
	for index := range want {
		if sequence[index] != want[index] {
			t.Fatalf("sequence=%v err=%v", sequence, err)
		}
	}
}

func TestRawStopPreservesTransportWhileImporterOwnsActiveModem(t *testing.T) {
	for _, code := range []string{"raw_usb_paid_call_active", "raw_usb_data_session_active"} {
		t.Run(code, func(t *testing.T) {
			sequence := []string{}
			agents := &fakeAgents{sequence: &sequence, stopError: &agentlink.RemoteError{
				Kind: "conflict", Code: code, Retryable: true,
			}}
			broker := &fakeBroker{sequence: &sequence}
			reconciler := &Reconciler{agents: agents, broker: broker, actionTimeout: time.Second}
			current := &session{
				sourceAgentID: "source", sourceProcessGeneration: "source-process",
				importerAgentID: "importer", importerProcessGeneration: "importer-process",
				attachmentID: "attachment", sessionGeneration: "sim-generation",
				equipmentID: "867530900000001", cardID: "8944100000000000001",
				usbSessionID: "usb-session", streamID: "stream",
			}
			if err := reconciler.stop(context.Background(), current); !importerOwnsActiveModem(err) {
				t.Fatalf("stop err=%v", err)
			}
			if len(sequence) != 1 || sequence[0] != "stop:importer" || len(broker.revoked) != 0 {
				t.Fatalf("sequence=%v revoked=%v", sequence, broker.revoked)
			}
			agents.stopError = nil
			if err := reconciler.stop(context.Background(), current); err != nil {
				t.Fatalf("stop after active owner released: %v", err)
			}
			want := []string{"stop:importer", "stop:importer", "stop:exporter", "revoke"}
			if len(sequence) != len(want) {
				t.Fatalf("sequence=%v", sequence)
			}
			for index := range want {
				if sequence[index] != want[index] {
					t.Fatalf("sequence=%v", sequence)
				}
			}
		})
	}
}

func TestRawSourceAllowsOtherModemSessionButRejectsSameEquipment(t *testing.T) {
	now := time.Now().UTC()
	_, _, binding := rawTestCatalog()
	statuses := rawTestStatuses(now, binding)
	statuses[0].Topology.RawUSBSessions = []agentlink.RawUSBSessionFact{{
		Role: agentlink.RawUSBExporter, EquipmentID: "867530900000099", CardID: "8944100000000000099",
	}}
	if _, err := resolveSource(binding, statuses, now, time.Minute); err != nil {
		t.Fatalf("unrelated modem session blocked source: %v", err)
	}
	statuses[0].Topology.RawUSBSessions[0].EquipmentID = binding.EquipmentID
	if _, err := resolveSource(binding, statuses, now, time.Minute); err == nil {
		t.Fatal("same equipment session was not rejected")
	}
}

func newRawTestReconciler(t *testing.T, catalog *fakeCatalog, agents *fakeAgents,
	broker *fakeBroker, now time.Time) *Reconciler {
	t.Helper()
	reconciler, err := New(Config{
		Context: context.Background(), Catalog: catalog, Agents: agents, Broker: broker,
		Interval: time.Second, ActionTimeout: time.Second, HandshakeTTL: 7 * time.Second,
		StartupGrace: 7 * time.Second, BaseBackoff: time.Second, MaximumBackoff: time.Minute,
		TopologyTTL: time.Minute, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func TestRawReconcilerHandshakeCoversBothSerialAgentActions(t *testing.T) {
	catalog, _, binding := rawTestCatalog()
	sequence := []string{}
	agents := &fakeAgents{statuses: rawTestStatuses(time.Now().UTC(), binding), sequence: &sequence}
	broker := &fakeBroker{sequence: &sequence}
	if _, err := New(Config{
		Context: context.Background(), Catalog: catalog, Agents: agents, Broker: broker,
		Interval: time.Second, ActionTimeout: 3 * time.Second, HandshakeTTL: 10 * time.Second,
		StartupGrace: time.Minute, BaseBackoff: time.Second, MaximumBackoff: time.Minute,
		TopologyTTL: time.Minute,
	}); err == nil {
		t.Fatal("handshake shorter than both serial action windows was accepted")
	}
	reconciler, err := New(Config{
		Context: context.Background(), Catalog: catalog, Agents: agents, Broker: broker,
		Interval: time.Second, ActionTimeout: 3 * time.Second,
		BaseBackoff: time.Second, MaximumBackoff: time.Minute, TopologyTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reconciler.handshakeTTL != 11*time.Second || reconciler.startupGrace != 41*time.Second {
		t.Fatalf("handshake=%s startup=%s", reconciler.handshakeTTL, reconciler.startupGrace)
	}
}

func rawTestCatalog() (*fakeCatalog, linecatalog.Line, linecatalog.RawModemBinding) {
	line := linecatalog.Line{
		SchemaVersion: linecatalog.SchemaVersion, ID: "line-raw", Enabled: true,
		CardID: "8944100000000000001", SIM: linecatalog.SIMConfig{IMEI: "867530900000001"},
	}
	binding := linecatalog.RawModemBinding{
		SchemaVersion: linecatalog.RawModemBindingSchemaVersion, Epoch: 2, LineID: line.ID,
		SourceAgentID: "source-agent", EquipmentID: line.SIM.IMEI, CardID: line.CardID,
		ImporterAgentID: "importer-agent", Enabled: true,
	}
	return &fakeCatalog{
		snapshot: linecatalog.Snapshot{SchemaVersion: linecatalog.SchemaVersion, Revision: 1, Lines: []linecatalog.Line{line}},
		raw: linecatalog.RawModemSnapshot{SchemaVersion: linecatalog.RawModemBindingSchemaVersion, Revision: 2,
			Bindings: []linecatalog.RawModemBinding{binding}},
	}, line, binding
}

func rawTestStatuses(now time.Time, binding linecatalog.RawModemBinding) []agentlink.ConnectionStatus {
	return []agentlink.ConnectionStatus{
		{
			AgentID: binding.SourceAgentID, ProcessGeneration: "source-process", LastReport: now,
			Topology: &agentlink.TopologySnapshot{
				ModemCondition: agentlink.ModemReady, RawUSBSource: true,
				Modems: []agentlink.ModemFact{{
					AttachmentID: "source-attachment", EquipmentID: binding.EquipmentID,
					AT:      agentlink.ModemATControlFact{State: "ready"},
					SIM:     agentlink.ModemSIMFact{State: "ready", ICCID: binding.CardID, SessionGeneration: "source-sim-generation"},
					Network: agentlink.ModemNetworkFact{Data: "disconnected", DataGuard: "protected"},
				}},
			},
		},
		{
			AgentID: binding.ImporterAgentID, ProcessGeneration: "importer-process", LastReport: now,
			Topology: &agentlink.TopologySnapshot{ModemCondition: agentlink.ModemStarting, RawUSBImporter: true},
		},
	}
}

func rawSessionFact(current *session, role agentlink.RawUSBRole) agentlink.RawUSBSessionFact {
	return agentlink.RawUSBSessionFact{
		Role: role, SourceAgentID: current.sourceAgentID,
		SourceProcessGeneration: current.sourceProcessGeneration,
		AttachmentID:            current.attachmentID, SessionGeneration: current.sessionGeneration,
		EquipmentID: current.equipmentID, CardID: current.cardID,
		USBSessionID: current.usbSessionID, State: "transport_active",
	}
}
