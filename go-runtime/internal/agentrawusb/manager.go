// Package agentrawusb owns whole-modem USB/IP sessions on one Agent process.
// The source Agent captures one exact physical modem; the service-host Agent
// imports that complete device. sing-usbip and sing-mux own the USB and
// multiplex protocols, while this package only applies MDD identity fences and
// lifetime cleanup.
package agentrawusb

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentusbip"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
)

type SourceTarget struct {
	SourceAgentID           string
	SourceProcessGeneration string
	AttachmentID            string
	SessionGeneration       string
	EquipmentID             string
	CardID                  string
	USBSessionID            string
}

// SourceBackend releases the adapted owner and reserves one exact local USB
// parent. ReleaseRawUSB makes the device available to the adapted prober again.
type SourceBackend interface {
	AcquireRawUSB(context.Context, SourceTarget) (physicalID string, err error)
	ReleaseRawUSB(SourceTarget) error
}

// ImportGuard is the Linux service-host boundary around sing-usbip's native
// VHCI attach. It must deny driver binding before StartImport, return the exact
// newly attached local USB parent, and keep that parent quarantined until
// StopImport completes the native detach.
type ImportGuard interface {
	StartImport(context.Context, rawusb.Device, func() error, func() error) (physicalID string, err error)
	StopImport(context.Context, string, func() error) error
}

type Exporter interface {
	Device() rawusb.Device
	ServeMultiplexed(context.Context, net.Conn) error
	Close() error
}

type Importer interface {
	Start() error
	Close() error
}

type Config struct {
	Context           context.Context
	ServerURL         string
	ServerToken       string
	AgentID           string
	ProcessGeneration string
	HTTPClient        *http.Client
	Topology          func() agentlink.TopologySnapshot
	Source            SourceBackend
	Coordinator       agentmodem.AuxiliaryCoordinator
	ImportGuard       ImportGuard

	Connect     func(context.Context, agentusbip.EndpointIdentity) (net.Conn, error)
	NewExporter func(context.Context, string) (Exporter, error)
	NewImporter func(context.Context, rawusb.Device, net.Conn) (Importer, error)
}

type Manager struct {
	config Config
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	sessions    map[string]*session
	byEquipment map[string]*session
	closed      bool
	wait        sync.WaitGroup
}

type session struct {
	request          agentlink.RawUSBRequest
	device           agentlink.RawUSBDevice
	importPhysicalID string
	ctx              context.Context
	cancel           context.CancelFunc
	lifecycle        sync.Mutex
	initDone         chan struct{}
	initOnce         sync.Once
	initErr          error
	exporter         Exporter
	importer         Importer
	transport        net.Conn
	sourceClaimed    bool
	closeOnce        sync.Once
	closeErr         error
}

func NewManager(config Config) (*Manager, error) {
	if config.Context == nil || config.HTTPClient == nil || len(config.ServerToken) < 32 ||
		strings.TrimSpace(config.AgentID) == "" || strings.TrimSpace(config.ProcessGeneration) == "" ||
		config.Source == nil && config.ImportGuard == nil ||
		(config.Source != nil || config.ImportGuard != nil) && config.Coordinator == nil ||
		config.Source != nil && config.Topology == nil {
		return nil, errors.New("invalid raw USB Agent configuration")
	}
	if config.Connect == nil {
		connector, err := agentusbip.NewConnector(config.ServerURL, config.ServerToken, config.HTTPClient)
		if err != nil {
			return nil, err
		}
		config.Connect = connector.Connect
	}
	if config.NewExporter == nil {
		config.NewExporter = func(ctx context.Context, physicalID string) (Exporter, error) {
			return rawusb.NewExporter(ctx, physicalID)
		}
	}
	if config.NewImporter == nil {
		config.NewImporter = func(ctx context.Context, device rawusb.Device, stream net.Conn) (Importer, error) {
			return rawusb.NewMultiplexedImporter(ctx, device, stream)
		}
	}
	ctx, cancel := context.WithCancel(config.Context)
	return &Manager{
		config: config, ctx: ctx, cancel: cancel,
		sessions: make(map[string]*session), byEquipment: make(map[string]*session),
	}, nil
}

func (manager *Manager) ExecuteRawUSB(ctx context.Context, request agentlink.RawUSBRequest) agentlink.RawUSBResponse {
	response := responseFor(request)
	if err := request.Validate(); err != nil {
		response.Failure = &agentlink.RemoteError{Kind: "rejected", Code: "invalid_raw_usb_request"}
		return response
	}
	var device agentlink.RawUSBDevice
	var err error
	switch request.Action {
	case agentlink.RawUSBExportStart:
		device, err = manager.startExporter(ctx, request)
		if err == nil {
			response.State, response.Device = "prepared", &device
		}
	case agentlink.RawUSBImportStart:
		device, err = manager.startImporter(ctx, request)
		if err == nil {
			response.State, response.Device = "starting", &device
		}
	case agentlink.RawUSBStop:
		err = manager.stop(ctx, request)
		if err == nil {
			response.State = "stopped"
		}
	}
	if err != nil {
		response.Failure = failureFor(err)
	}
	return response
}

// Topology appends transport ownership facts without duplicating the source
// SIM as a normal modem. The importing Agent's ordinary modem prober remains
// the only authority that can publish a usable imported modem/card route.
func (manager *Manager) Topology(base agentlink.TopologySnapshot) agentlink.TopologySnapshot {
	if manager == nil {
		return base
	}
	manager.mu.Lock()
	base.RawUSBSource = manager.config.Source != nil
	base.RawUSBImporter = manager.config.ImportGuard != nil
	sessions := make([]*session, 0, len(manager.sessions))
	for _, current := range manager.sessions {
		sessions = append(sessions, current)
	}
	manager.mu.Unlock()
	for _, current := range sessions {
		select {
		case <-current.initDone:
			if current.initErr != nil || current.ctx.Err() != nil {
				continue
			}
			request := current.request
			base.RawUSBSessions = append(base.RawUSBSessions, agentlink.RawUSBSessionFact{
				Role: request.Role, SourceAgentID: request.SourceAgentID,
				SourceProcessGeneration: request.SourceProcessGeneration,
				AttachmentID:            request.AttachmentID, SessionGeneration: request.SessionGeneration,
				EquipmentID: request.EquipmentID, CardID: request.CardID,
				USBSessionID: request.USBSessionID, State: "transport_active",
			})
		default:
		}
	}
	return agentlink.NormalizeTopology(base)
}

func (manager *Manager) startExporter(ctx context.Context, request agentlink.RawUSBRequest) (agentlink.RawUSBDevice, error) {
	if manager.config.Source == nil || request.SourceAgentID != manager.config.AgentID ||
		request.SourceProcessGeneration != manager.config.ProcessGeneration {
		return agentlink.RawUSBDevice{}, errors.New("this Agent is not the exact raw USB source")
	}
	if err := validateSourceTopology(manager.config.Topology(), request); err != nil {
		return agentlink.RawUSBDevice{}, err
	}
	current, existing, err := manager.reserve(request)
	if err != nil {
		return agentlink.RawUSBDevice{}, err
	}
	if existing {
		return awaitInitialization(ctx, current)
	}
	target := sourceTarget(request)
	current.lifecycle.Lock()
	err = manager.config.Coordinator.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
		if err := validateSourceTopology(manager.config.Topology(), request); err != nil {
			return err
		}
		physicalID, acquireErr := manager.config.Source.AcquireRawUSB(operationContext, target)
		if acquireErr != nil {
			return acquireErr
		}
		current.sourceClaimed = true
		exporter, exportErr := manager.config.NewExporter(current.ctx, physicalID)
		if exportErr != nil {
			return exportErr
		}
		current.exporter = exporter
		current.device = fromDevice(exporter.Device())
		if !validDevice(current.device) {
			return errors.New("sing-usbip returned an invalid exported device")
		}
		if current.ctx.Err() != nil {
			return current.ctx.Err()
		}
		return nil
	})
	current.lifecycle.Unlock()
	completeInitialization(current, err)
	if err != nil {
		manager.finish(current)
		return agentlink.RawUSBDevice{}, err
	}
	manager.wait.Add(1)
	go func() {
		defer manager.wait.Done()
		stream, connectErr := manager.config.Connect(current.ctx, endpointFor(manager.config, request))
		if connectErr == nil {
			current.lifecycle.Lock()
			if current.ctx.Err() == nil {
				current.transport = stream
			} else {
				connectErr = current.ctx.Err()
			}
			current.lifecycle.Unlock()
			if connectErr == nil {
				connectErr = current.exporter.ServeMultiplexed(current.ctx, stream)
			} else {
				_ = stream.Close()
			}
		}
		_ = connectErr
		manager.finish(current)
	}()
	return current.device, nil
}

func (manager *Manager) startImporter(ctx context.Context, request agentlink.RawUSBRequest) (agentlink.RawUSBDevice, error) {
	if manager.config.ImportGuard == nil || request.SourceAgentID == manager.config.AgentID {
		return agentlink.RawUSBDevice{}, errors.New("this Agent is not an eligible raw USB importer")
	}
	current, existing, err := manager.reserve(request)
	if err != nil {
		return agentlink.RawUSBDevice{}, err
	}
	if existing {
		return awaitInitialization(ctx, current)
	}
	current.lifecycle.Lock()
	connectContext, cancelConnect := context.WithCancel(ctx)
	stopConnect := context.AfterFunc(current.ctx, cancelConnect)
	stream, err := manager.config.Connect(connectContext, endpointFor(manager.config, request))
	stopConnect()
	cancelConnect()
	if err != nil {
		current.lifecycle.Unlock()
		completeInitialization(current, err)
		manager.finish(current)
		return agentlink.RawUSBDevice{}, err
	}
	observed := &observedConn{Conn: stream, failed: current.cancel}
	current.transport = observed
	device := toDevice(*request.Device)
	importer, err := manager.config.NewImporter(current.ctx, device, observed)
	if err == nil {
		current.importer = importer
		current.importPhysicalID, err = manager.config.ImportGuard.StartImport(current.ctx, device, importer.Start, importer.Close)
	}
	if err != nil {
		// StartImport owns rollback after native attach is attempted. Close is
		// nevertheless repeated here because the guard can reject its preflight
		// before invoking start; sing-usbip's importer close is idempotent.
		if importer != nil {
			err = errors.Join(err, importer.Close())
		}
		current.importer = nil
		current.lifecycle.Unlock()
		completeInitialization(current, err)
		manager.finish(current)
		return agentlink.RawUSBDevice{}, err
	}
	current.importer, current.device = importer, *request.Device
	if current.ctx.Err() != nil {
		err = current.ctx.Err()
	}
	current.lifecycle.Unlock()
	completeInitialization(current, err)
	if err != nil {
		manager.finish(current)
		return agentlink.RawUSBDevice{}, err
	}
	manager.wait.Add(1)
	go func() {
		defer manager.wait.Done()
		<-current.ctx.Done()
		manager.finish(current)
	}()
	return current.device, nil
}

func (manager *Manager) reserve(request agentlink.RawUSBRequest) (*session, bool, error) {
	key := sessionKey(request.Role, request.USBSessionID)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil, false, context.Canceled
	}
	if current := manager.sessions[key]; current != nil {
		if sameSession(current.request, request) {
			if current.ctx.Err() != nil {
				return nil, false, context.Canceled
			}
			return current, true, nil
		}
		return nil, false, errors.New("raw USB session identity was replaced")
	}
	if request.Role == agentlink.RawUSBExporter {
		if manager.byEquipment[request.EquipmentID] != nil {
			return nil, false, errors.New("another raw USB session owns this modem")
		}
	}
	sessionContext, cancel := context.WithCancel(manager.ctx)
	current := &session{request: request, ctx: sessionContext, cancel: cancel, initDone: make(chan struct{})}
	manager.sessions[key] = current
	if request.Role == agentlink.RawUSBExporter {
		manager.byEquipment[request.EquipmentID] = current
	}
	return current, false, nil
}

func (manager *Manager) stop(ctx context.Context, request agentlink.RawUSBRequest) error {
	key := sessionKey(request.Role, request.USBSessionID)
	manager.mu.Lock()
	current := manager.sessions[key]
	manager.mu.Unlock()
	if current == nil {
		return nil
	}
	if !sameStopTarget(current.request, request) {
		return errors.New("raw USB stop target does not match the current session")
	}
	// A normal detach must never cut the only control path while a paid-call
	// lease exists on the importing Agent. Transport failure is handled by the
	// source-side durable recovery fence instead of passing through this path.
	return manager.config.Coordinator.DoAuxiliary(ctx, request.EquipmentID, func(context.Context) error {
		return manager.finish(current)
	})
}

func (manager *Manager) finish(current *session) error {
	if current == nil {
		return nil
	}
	current.cancel()
	current.closeOnce.Do(func() {
		current.lifecycle.Lock()
		defer current.lifecycle.Unlock()
		if current.importer != nil {
			if manager.config.ImportGuard != nil && current.importPhysicalID != "" {
				stopContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				current.closeErr = errors.Join(current.closeErr, manager.config.ImportGuard.StopImport(stopContext,
					current.importPhysicalID, current.importer.Close))
				cancel()
			} else {
				current.closeErr = errors.Join(current.closeErr, current.importer.Close())
			}
			// The importer owns the h2mux client and therefore its one underlying
			// WSS. Do not close the same transport again and turn a successful
			// physical detach into a spurious "already closed" failure.
			current.transport = nil
		}
		if current.transport != nil {
			current.closeErr = errors.Join(current.closeErr, current.transport.Close())
		}
		if current.exporter != nil {
			current.closeErr = errors.Join(current.closeErr, current.exporter.Close())
		}
		if current.sourceClaimed {
			current.closeErr = errors.Join(current.closeErr, manager.config.Source.ReleaseRawUSB(sourceTarget(current.request)))
		}
		manager.mu.Lock()
		key := sessionKey(current.request.Role, current.request.USBSessionID)
		if manager.sessions[key] == current {
			delete(manager.sessions, key)
		}
		if manager.byEquipment[current.request.EquipmentID] == current {
			delete(manager.byEquipment, current.request.EquipmentID)
		}
		manager.mu.Unlock()
	})
	return current.closeErr
}

func (manager *Manager) Close() error {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closed = true
	manager.cancel()
	sessions := make([]*session, 0, len(manager.sessions))
	for _, current := range manager.sessions {
		sessions = append(sessions, current)
	}
	manager.mu.Unlock()
	var failures []error
	for _, current := range sessions {
		failures = append(failures, manager.finish(current))
	}
	manager.wait.Wait()
	return errors.Join(failures...)
}

func validateSourceTopology(topology agentlink.TopologySnapshot, request agentlink.RawUSBRequest) error {
	if topology.ModemCondition != agentlink.ModemReady {
		return errors.New("source modem topology is not ready")
	}
	matches := 0
	for _, modem := range topology.Modems {
		if modem.AttachmentID == request.AttachmentID && modem.EquipmentID == request.EquipmentID &&
			modem.SIM.State == "ready" && modem.SIM.ICCID == request.CardID &&
			modem.SIM.SessionGeneration == request.SessionGeneration {
			matches++
		}
	}
	if matches != 1 {
		return errors.New("source modem or inserted SIM generation was replaced")
	}
	return nil
}

func endpointFor(config Config, request agentlink.RawUSBRequest) agentusbip.EndpointIdentity {
	return agentusbip.EndpointIdentity{
		SessionIdentity: agentusbip.SessionIdentity{
			SourceAgentID: request.SourceAgentID, SourceProcessGeneration: request.SourceProcessGeneration,
			AttachmentID: request.AttachmentID, SessionGeneration: request.SessionGeneration,
			EquipmentID: request.EquipmentID, CardID: request.CardID,
			USBSessionID: request.USBSessionID, StreamID: request.StreamID,
		},
		Role: agentusbip.Role(request.Role), AgentID: config.AgentID,
		ProcessGeneration: config.ProcessGeneration, StreamToken: request.StreamToken,
	}
}

func responseFor(request agentlink.RawUSBRequest) agentlink.RawUSBResponse {
	return agentlink.RawUSBResponse{
		OperationID: request.OperationID, Action: request.Action, Role: request.Role,
		SourceAgentID: request.SourceAgentID, SourceProcessGeneration: request.SourceProcessGeneration,
		AttachmentID: request.AttachmentID, SessionGeneration: request.SessionGeneration,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
		USBSessionID: request.USBSessionID, StreamID: request.StreamID,
	}
}

func sourceTarget(request agentlink.RawUSBRequest) SourceTarget {
	return SourceTarget{
		SourceAgentID: request.SourceAgentID, SourceProcessGeneration: request.SourceProcessGeneration,
		AttachmentID: request.AttachmentID, SessionGeneration: request.SessionGeneration,
		EquipmentID: request.EquipmentID, CardID: request.CardID, USBSessionID: request.USBSessionID,
	}
}

func sessionKey(role agentlink.RawUSBRole, id string) string { return string(role) + "/" + id }

func sameSession(left, right agentlink.RawUSBRequest) bool {
	left.OperationID, right.OperationID = "", ""
	return left.Action == right.Action && left.Role == right.Role && left.SourceAgentID == right.SourceAgentID &&
		left.SourceProcessGeneration == right.SourceProcessGeneration && left.AttachmentID == right.AttachmentID &&
		left.SessionGeneration == right.SessionGeneration && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID && left.USBSessionID == right.USBSessionID && left.StreamID == right.StreamID &&
		left.StreamToken == right.StreamToken && equalDevice(left.Device, right.Device)
}

func sameStopTarget(start, stop agentlink.RawUSBRequest) bool {
	return start.Role == stop.Role && start.SourceAgentID == stop.SourceAgentID &&
		start.SourceProcessGeneration == stop.SourceProcessGeneration && start.AttachmentID == stop.AttachmentID &&
		start.SessionGeneration == stop.SessionGeneration && start.EquipmentID == stop.EquipmentID &&
		start.CardID == stop.CardID && start.USBSessionID == stop.USBSessionID
}

func equalDevice(left, right *agentlink.RawUSBDevice) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func fromDevice(device rawusb.Device) agentlink.RawUSBDevice {
	return agentlink.RawUSBDevice{BusID: device.BusID, VendorID: device.VendorID, ProductID: device.ProductID, Serial: device.Serial}
}

func toDevice(device agentlink.RawUSBDevice) rawusb.Device {
	return rawusb.Device{BusID: device.BusID, VendorID: device.VendorID, ProductID: device.ProductID, Serial: device.Serial}
}

func validDevice(device agentlink.RawUSBDevice) bool {
	request := agentlink.RawUSBRequest{
		OperationID: "validation", Action: agentlink.RawUSBImportStart, Role: agentlink.RawUSBImporter,
		SourceAgentID: "validation", SourceProcessGeneration: "validation", AttachmentID: "validation",
		SessionGeneration: "validation", EquipmentID: "12345678901234", CardID: "123456789012345678",
		USBSessionID: "validation", StreamID: "validation", StreamToken: strings.Repeat("x", 32), Device: &device,
	}
	return request.Validate() == nil
}

func completeInitialization(current *session, err error) {
	current.initOnce.Do(func() {
		current.initErr = err
		close(current.initDone)
	})
}

func awaitInitialization(ctx context.Context, current *session) (agentlink.RawUSBDevice, error) {
	select {
	case <-ctx.Done():
		return agentlink.RawUSBDevice{}, ctx.Err()
	case <-current.initDone:
		current.lifecycle.Lock()
		defer current.lifecycle.Unlock()
		if current.initErr != nil {
			return agentlink.RawUSBDevice{}, current.initErr
		}
		if err := current.ctx.Err(); err != nil {
			return agentlink.RawUSBDevice{}, err
		}
		return current.device, nil
	}
}

func failureFor(err error) *agentlink.RemoteError {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &agentlink.RemoteError{Kind: "transport", Code: "raw_usb_timeout", Retryable: true}
	case strings.Contains(err.Error(), "owns"), strings.Contains(err.Error(), "replaced"), strings.Contains(err.Error(), "match"):
		return &agentlink.RemoteError{Kind: "conflict", Code: "raw_usb_identity_conflict"}
	case strings.Contains(err.Error(), "eligible"), strings.Contains(err.Error(), "not the exact"):
		return &agentlink.RemoteError{Kind: "rejected", Code: "raw_usb_role_rejected"}
	case strings.Contains(err.Error(), "paid-call lease"):
		return &agentlink.RemoteError{Kind: "conflict", Code: "raw_usb_paid_call_active", Retryable: true}
	case errors.Is(err, agentdata.ErrSessionActive):
		return &agentlink.RemoteError{Kind: "conflict", Code: "raw_usb_data_session_active", Retryable: true}
	default:
		return &agentlink.RemoteError{Kind: "failed", Code: "raw_usb_failed", Retryable: true}
	}
}

type observedConn struct {
	net.Conn
	failed func()
	once   sync.Once
}

func (conn *observedConn) Read(payload []byte) (int, error) {
	n, err := conn.Conn.Read(payload)
	if err != nil {
		conn.once.Do(conn.failed)
	}
	return n, err
}

func (conn *observedConn) Write(payload []byte) (int, error) {
	n, err := conn.Conn.Write(payload)
	if err != nil {
		conn.once.Do(conn.failed)
	}
	return n, err
}

func (conn *observedConn) Close() error {
	conn.once.Do(conn.failed)
	return conn.Conn.Close()
}

var _ agentlink.RawUSBExecutor = (*Manager)(nil)
