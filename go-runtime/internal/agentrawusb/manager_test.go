package agentrawusb

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentusbip"
)

type testSource struct{}

func (testSource) AcquireRawUSB(context.Context, SourceTarget) (string, error) {
	return "physical-a", nil
}

func (testSource) ReleaseRawUSB(SourceTarget) error { return nil }

type testCoordinator struct{}

func (testCoordinator) DoAuxiliary(ctx context.Context, _ string, callback func(context.Context) error) error {
	return callback(ctx)
}

func TestCanceledSessionCannotReturnStalePreparedSuccess(t *testing.T) {
	manager, err := NewManager(Config{
		Context: context.Background(), ServerToken: strings.Repeat("s", 32),
		AgentID: "agent-a", ProcessGeneration: "process-a", HTTPClient: http.DefaultClient,
		Topology: func() agentlink.TopologySnapshot { return agentlink.TopologySnapshot{} },
		Source:   testSource{}, Coordinator: testCoordinator{},
		Connect: func(context.Context, agentusbip.EndpointIdentity) (net.Conn, error) { return nil, context.Canceled },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	request := testExporterRequest()
	current, existing, err := manager.reserve(request)
	if err != nil || existing {
		t.Fatalf("reserve existing=%t err=%v", existing, err)
	}
	current.device = agentlink.RawUSBDevice{BusID: "1-2", VendorID: 0x2c7c, ProductID: 0x0125}
	completeInitialization(current, nil)
	current.cancel()
	if _, existing, err := manager.reserve(request); !errors.Is(err, context.Canceled) || existing {
		t.Fatalf("duplicate canceled reserve existing=%t err=%v", existing, err)
	}
	if _, err := awaitInitialization(context.Background(), current); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initialized session returned err=%v", err)
	}
}

func TestRawTopologyDoesNotDuplicateSourceSIMAsModem(t *testing.T) {
	manager := &Manager{
		config:   Config{Source: testSource{}},
		sessions: map[string]*session{}, byEquipment: map[string]*session{},
	}
	request := testExporterRequest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := &session{request: request, ctx: ctx, cancel: cancel, initDone: make(chan struct{})}
	completeInitialization(current, nil)
	manager.sessions[sessionKey(request.Role, request.USBSessionID)] = current
	base := agentlink.TopologySnapshot{
		ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{},
		ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{},
	}
	got := manager.Topology(base)
	if !got.RawUSBSource || got.RawUSBImporter || len(got.RawUSBSessions) != 1 || len(got.Modems) != 0 {
		t.Fatalf("topology=%+v", got)
	}
	if got.RawUSBSessions[0].CardID != request.CardID || got.RawUSBSessions[0].EquipmentID != request.EquipmentID {
		t.Fatalf("raw session=%+v", got.RawUSBSessions[0])
	}
}

func testExporterRequest() agentlink.RawUSBRequest {
	return agentlink.RawUSBRequest{
		OperationID: "operation-a", Action: agentlink.RawUSBExportStart, Role: agentlink.RawUSBExporter,
		SourceAgentID: "agent-a", SourceProcessGeneration: "process-a",
		AttachmentID: "attachment-a", SessionGeneration: "card-generation-a",
		EquipmentID: "867530900000001", CardID: "8944100000000000001",
		USBSessionID: "usb-session-a", StreamID: "stream-a", StreamToken: strings.Repeat("x", 32),
	}
}
