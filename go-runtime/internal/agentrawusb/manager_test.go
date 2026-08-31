package agentrawusb

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentusbip"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
)

type testSource struct{}

func (testSource) AcquireRawUSB(context.Context, SourceTarget) (SourceClaim, error) {
	return SourceClaim{PhysicalID: "physical-a"}, nil
}

func (testSource) RecordRawUSBDevice(SourceTarget, rawusb.Device) error { return nil }

func (testSource) PreserveRawUSB(SourceTarget, Exporter) error { return nil }

func (testSource) TakeRawUSB(SourceTarget) (Exporter, bool, error) { return nil, false, nil }

func (testSource) ReleaseRawUSB(SourceTarget) error { return nil }

func (testSource) RecoveryRawUSB() []agentlink.RawUSBRecoveryFact { return nil }

type testCoordinator struct{}

func (testCoordinator) DoAuxiliary(ctx context.Context, _ string, callback func(context.Context) error) error {
	return callback(ctx)
}

type durableTestSource struct {
	exporter       *durableTestExporter
	recordErr      error
	cancelOnRecord context.CancelFunc
	preserved      chan struct{}
	preserveOnce   sync.Once
	preserveCalls  atomic.Uint32
}

func (source *durableTestSource) AcquireRawUSB(context.Context, SourceTarget) (SourceClaim, error) {
	return SourceClaim{}, nil
}
func (source *durableTestSource) RecordRawUSBDevice(SourceTarget, rawusb.Device) error {
	if source.cancelOnRecord != nil {
		source.cancelOnRecord()
	}
	return source.recordErr
}
func (source *durableTestSource) PreserveRawUSB(SourceTarget, Exporter) error {
	source.preserveCalls.Add(1)
	source.preserveOnce.Do(func() { close(source.preserved) })
	return nil
}
func (source *durableTestSource) TakeRawUSB(SourceTarget) (Exporter, bool, error) {
	return source.exporter, true, nil
}
func (*durableTestSource) ReleaseRawUSB(SourceTarget) error               { return nil }
func (*durableTestSource) RecoveryRawUSB() []agentlink.RawUSBRecoveryFact { return nil }

type durableTestExporter struct {
	device rawusb.Device
	closed atomic.Uint32
}

func (exporter *durableTestExporter) Device() rawusb.Device                   { return exporter.device }
func (*durableTestExporter) ServeMultiplexed(context.Context, net.Conn) error { return nil }
func (exporter *durableTestExporter) Close() error {
	exporter.closed.Add(1)
	return nil
}

func TestBorrowedPersistentExporterIsPreservedAcrossEveryStartFailure(t *testing.T) {
	tests := []struct {
		name           string
		device         rawusb.Device
		recordErr      error
		cancelOnRecord bool
		connectErr     error
		expectStartErr bool
	}{
		{name: "invalid device", device: rawusb.Device{}, expectStartErr: true},
		{name: "record failure", device: rawusb.Device{BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125},
			recordErr: errors.New("record failed"), expectStartErr: true},
		{name: "context canceled", device: rawusb.Device{BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125},
			cancelOnRecord: true, expectStartErr: true},
		{name: "connect failure", device: rawusb.Device{BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125},
			connectErr: errors.New("connect failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			exporter := &durableTestExporter{device: test.device}
			source := &durableTestSource{exporter: exporter, recordErr: test.recordErr, preserved: make(chan struct{})}
			if test.cancelOnRecord {
				source.cancelOnRecord = cancel
			}
			request := testExporterRequest()
			request.Recovering = true
			manager, err := NewManager(Config{
				Context: parent, ServerToken: strings.Repeat("s", 32), AgentID: "agent-a",
				ProcessGeneration: "process-a", HTTPClient: http.DefaultClient,
				Topology: func() agentlink.TopologySnapshot {
					return agentlink.TopologySnapshot{RawUSBSource: true, RawUSBRecoveries: []agentlink.RawUSBRecoveryFact{{
						AttachmentID: request.AttachmentID, SessionGeneration: request.SessionGeneration,
						EquipmentID: request.EquipmentID, CardID: request.CardID,
						USBSessionID: request.USBSessionID, CaptureGeneration: request.CaptureGeneration,
						Device: agentlink.RawUSBDevice{BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125},
						State:  "capture_reserved",
					}}}
				},
				Source: source, Coordinator: testCoordinator{},
				Connect: func(context.Context, agentusbip.EndpointIdentity) (net.Conn, error) {
					return nil, test.connectErr
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, startErr := manager.startExporter(context.Background(), request)
			if test.expectStartErr && startErr == nil {
				t.Fatal("start unexpectedly succeeded")
			}
			select {
			case <-source.preserved:
			case <-time.After(2 * time.Second):
				t.Fatal("borrowed persistent exporter was not returned")
			}
			if exporter.closed.Load() != 0 || source.preserveCalls.Load() != 1 {
				t.Fatalf("closed=%d preserved=%d", exporter.closed.Load(), source.preserveCalls.Load())
			}
			_ = manager.Close()
		})
	}
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
		USBSessionID: "usb-session-a", CaptureGeneration: "capture-a",
		StreamID: "stream-a", StreamToken: strings.Repeat("x", 32),
	}
}
