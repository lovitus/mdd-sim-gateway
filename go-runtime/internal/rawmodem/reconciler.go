// Package rawmodem reconciles one durable raw-modem binding into one
// whole-device sing-usbip session. It is intentionally level-triggered and
// keeps only ephemeral transport ownership in memory: linecatalog owns intent,
// Agents own hardware facts, and sing-usbip owns USB/IP lifecycle.
package rawmodem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentusbip"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const (
	defaultInterval       = 2 * time.Second
	defaultActionTimeout  = 30 * time.Second
	handshakeMargin       = 5 * time.Second
	defaultStartupMargin  = 30 * time.Second
	defaultBaseBackoff    = time.Second
	defaultMaximumBackoff = time.Minute
	defaultTopologyTTL    = 30 * time.Second
	maximumHandshakeTTL   = 2 * time.Minute
)

type Catalog interface {
	Snapshot() (linecatalog.Snapshot, error)
	Get(string) (linecatalog.Line, error)
	RawModemBindings() (linecatalog.RawModemSnapshot, error)
}

type AgentControl interface {
	Statuses() []agentlink.ConnectionStatus
	ExecuteRawUSB(context.Context, string, string, agentlink.RawUSBRequest) (agentlink.RawUSBResponse, error)
}

type StreamBroker interface {
	Reserve(agentusbip.Reservation) error
	Revoke(string)
}

type Config struct {
	Context        context.Context
	Catalog        Catalog
	Agents         AgentControl
	Broker         StreamBroker
	Interval       time.Duration
	ActionTimeout  time.Duration
	HandshakeTTL   time.Duration
	StartupGrace   time.Duration
	BaseBackoff    time.Duration
	MaximumBackoff time.Duration
	TopologyTTL    time.Duration
	Now            func() time.Time
	Logf           func(string, ...any)
}

type Reconciler struct {
	catalog        Catalog
	agents         AgentControl
	broker         StreamBroker
	ctx            context.Context
	cancel         context.CancelFunc
	interval       time.Duration
	actionTimeout  time.Duration
	handshakeTTL   time.Duration
	startupGrace   time.Duration
	baseBackoff    time.Duration
	maximumBackoff time.Duration
	topologyTTL    time.Duration
	now            func() time.Time
	logf           func(string, ...any)
	wake           chan struct{}

	startOnce sync.Once
	closeOnce sync.Once
	wg        sync.WaitGroup
	states    map[string]*lineState
}

type lineState struct {
	session  *session
	failures uint32
	retryAt  time.Time
}

type session struct {
	lineID                    string
	bindingKey                string
	sourceAgentID             string
	sourceProcessGeneration   string
	importerAgentID           string
	importerProcessGeneration string
	attachmentID              string
	sessionGeneration         string
	equipmentID               string
	cardID                    string
	usbSessionID              string
	captureGeneration         string
	streamID                  string
	recovering                bool
	startedAt                 time.Time
}

type sourceTarget struct {
	status            agentlink.ConnectionStatus
	modem             agentlink.ModemFact
	recovering        bool
	captureGeneration string
}

type desiredBinding struct {
	line    linecatalog.Line
	binding linecatalog.RawModemBinding
}

func New(config Config) (*Reconciler, error) {
	if config.Context == nil || config.Catalog == nil || config.Agents == nil || config.Broker == nil {
		return nil, errors.New("invalid raw modem reconciler dependencies")
	}
	if config.Interval == 0 {
		config.Interval = defaultInterval
	}
	if config.ActionTimeout == 0 {
		config.ActionTimeout = defaultActionTimeout
	}
	minimumHandshakeTTL := 2*config.ActionTimeout + handshakeMargin
	if config.HandshakeTTL == 0 {
		config.HandshakeTTL = minimumHandshakeTTL
	}
	if config.StartupGrace == 0 {
		config.StartupGrace = config.HandshakeTTL + defaultStartupMargin
	}
	if config.BaseBackoff == 0 {
		config.BaseBackoff = defaultBaseBackoff
	}
	if config.MaximumBackoff == 0 {
		config.MaximumBackoff = defaultMaximumBackoff
	}
	if config.TopologyTTL == 0 {
		config.TopologyTTL = defaultTopologyTTL
	}
	if config.Interval < 100*time.Millisecond || config.ActionTimeout < time.Second ||
		config.HandshakeTTL < minimumHandshakeTTL || config.HandshakeTTL > maximumHandshakeTTL ||
		config.StartupGrace < config.HandshakeTTL || config.BaseBackoff < 100*time.Millisecond ||
		config.MaximumBackoff < config.BaseBackoff || config.TopologyTTL < time.Second {
		return nil, errors.New("invalid raw modem reconciler timing")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Logf == nil {
		config.Logf = func(string, ...any) {}
	}
	ctx, cancel := context.WithCancel(config.Context)
	return &Reconciler{
		catalog: config.Catalog, agents: config.Agents, broker: config.Broker,
		ctx: ctx, cancel: cancel,
		interval: config.Interval, actionTimeout: config.ActionTimeout,
		handshakeTTL: config.HandshakeTTL, startupGrace: config.StartupGrace,
		baseBackoff: config.BaseBackoff, maximumBackoff: config.MaximumBackoff,
		topologyTTL: config.TopologyTTL, now: config.Now, logf: config.Logf,
		wake: make(chan struct{}, 1), states: make(map[string]*lineState),
	}, nil
}

func (reconciler *Reconciler) Start() {
	reconciler.startOnce.Do(func() {
		reconciler.wg.Add(1)
		go reconciler.run()
	})
}

func (reconciler *Reconciler) Close() {
	if reconciler == nil {
		return
	}
	reconciler.closeOnce.Do(reconciler.cancel)
	reconciler.wg.Wait()
}

func (reconciler *Reconciler) Wake() {
	if reconciler == nil {
		return
	}
	select {
	case reconciler.wake <- struct{}{}:
	default:
	}
}

func (reconciler *Reconciler) run() {
	defer reconciler.wg.Done()
	ticker := time.NewTicker(reconciler.interval)
	defer ticker.Stop()
	for {
		if err := reconciler.reconcile(reconciler.ctx); err != nil && !errors.Is(err, context.Canceled) {
			reconciler.logf("raw modem reconcile: %v", err)
		}
		select {
		case <-reconciler.ctx.Done():
			return
		case <-ticker.C:
		case <-reconciler.wake:
		}
	}
}

func (reconciler *Reconciler) reconcile(ctx context.Context) error {
	snapshot, err := reconciler.catalog.Snapshot()
	if err != nil {
		return fmt.Errorf("read line catalog: %w", err)
	}
	now := reconciler.now().UTC()
	statuses := reconciler.agents.Statuses()
	rawSnapshot, err := reconciler.catalog.RawModemBindings()
	if err != nil {
		return fmt.Errorf("read raw modem bindings: %w", err)
	}
	lines := make(map[string]linecatalog.Line, len(snapshot.Lines))
	for _, line := range snapshot.Lines {
		lines[line.ID] = line
	}
	desired := make(map[string]desiredBinding)
	for _, binding := range rawSnapshot.Bindings {
		line, exists := lines[binding.LineID]
		if exists && line.Enabled && binding.Enabled && line.CardID == binding.CardID && line.SIM.IMEI == binding.EquipmentID {
			desired[line.ID] = desiredBinding{line: line, binding: binding}
		}
	}
	var failures []error
	for lineID, state := range reconciler.states {
		item, exists := desired[lineID]
		if exists && state.session != nil && state.session.bindingKey == bindingKey(item.binding) {
			continue
		}
		if state.session != nil {
			if stopErr := reconciler.stop(ctx, state.session); stopErr != nil {
				failures = append(failures, fmt.Errorf("line %s stop replaced raw modem session: %w", lineID, stopErr))
				continue
			}
		}
		delete(reconciler.states, lineID)
	}
	for _, line := range snapshot.Lines {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, exists := desired[line.ID]
		if !exists {
			continue
		}
		state := reconciler.states[line.ID]
		if state == nil {
			state = &lineState{}
			reconciler.states[line.ID] = state
		}
		if state.session != nil {
			if sessionReady(state.session, statuses, now, reconciler.topologyTTL) {
				state.failures, state.retryAt = 0, time.Time{}
				continue
			}
			if now.Before(state.session.startedAt.Add(reconciler.startupGrace)) {
				continue
			}
			if stopErr := reconciler.stop(ctx, state.session); stopErr != nil {
				failures = append(failures, fmt.Errorf("line %s stop unhealthy raw modem session: %w", line.ID, stopErr))
				continue
			}
			state.session = nil
			reconciler.failed(state, now)
			continue
		}
		if now.Before(state.retryAt) {
			continue
		}
		started, startErr := reconciler.start(ctx, item, statuses, now)
		if startErr != nil {
			reconciler.failed(state, now)
			failures = append(failures, fmt.Errorf("line %s start raw modem session: %w", line.ID, startErr))
			continue
		}
		state.session = started
	}
	return errors.Join(failures...)
}

func (reconciler *Reconciler) start(ctx context.Context, item desiredBinding,
	statuses []agentlink.ConnectionStatus, now time.Time) (*session, error) {
	if err := reconciler.bindingStillCurrent(item); err != nil {
		return nil, err
	}
	source, err := resolveSource(item.binding, statuses, now, reconciler.topologyTTL)
	if err != nil {
		return nil, err
	}
	importer, err := resolveImporter(item.binding.ImporterAgentID, item.binding.SourceAgentID,
		statuses, now, reconciler.topologyTTL)
	if err != nil {
		return nil, err
	}
	usbSessionID, err := randomID("usb-session-")
	if err != nil {
		return nil, err
	}
	streamID, err := randomID("usb-stream-")
	if err != nil {
		return nil, err
	}
	exporterToken, err := agentusbip.NewStreamToken()
	if err != nil {
		return nil, err
	}
	importerToken, err := agentusbip.NewStreamToken()
	if err != nil {
		return nil, err
	}
	current := &session{
		lineID: item.line.ID, bindingKey: bindingKey(item.binding),
		sourceAgentID: source.status.AgentID, sourceProcessGeneration: source.status.ProcessGeneration,
		importerAgentID: importer.AgentID, importerProcessGeneration: importer.ProcessGeneration,
		attachmentID: source.modem.AttachmentID, sessionGeneration: source.modem.SIM.SessionGeneration,
		equipmentID: source.modem.EquipmentID, cardID: source.modem.SIM.ICCID,
		usbSessionID: usbSessionID, streamID: streamID, recovering: source.recovering,
		captureGeneration: source.captureGeneration, startedAt: now,
	}
	identity := current.identity()
	if err := reconciler.bindingStillCurrent(item); err != nil {
		return nil, err
	}
	if err := reconciler.broker.Reserve(agentusbip.Reservation{
		SessionIdentity: identity, ImporterAgentID: current.importerAgentID,
		ImporterProcessGeneration: current.importerProcessGeneration,
		ExporterStreamToken:       exporterToken, ImporterStreamToken: importerToken,
		ExpiresAt: now.Add(reconciler.handshakeTTL),
	}); err != nil {
		return nil, err
	}
	reserved := true
	exporterStarted, importerStarted := false, false
	defer func() {
		if reserved {
			reconciler.cleanupPartial(current, exporterStarted, importerStarted)
		}
	}()
	if err := reconciler.bindingStillCurrent(item); err != nil {
		return nil, err
	}
	actionContext, cancel := context.WithTimeout(ctx, reconciler.actionTimeout)
	exportResponse, err := reconciler.agents.ExecuteRawUSB(actionContext,
		current.sourceAgentID, current.sourceProcessGeneration,
		current.startRequest(agentlink.RawUSBExporter, exporterToken, nil))
	cancel()
	if err != nil || exportResponse.Device == nil {
		if err == nil {
			err = errors.New("source Agent returned no exported USB device")
		}
		return nil, err
	}
	exporterStarted = true
	device := *exportResponse.Device
	if err := reconciler.bindingStillCurrent(item); err != nil {
		return nil, err
	}
	actionContext, cancel = context.WithTimeout(ctx, reconciler.actionTimeout)
	_, err = reconciler.agents.ExecuteRawUSB(actionContext,
		current.importerAgentID, current.importerProcessGeneration,
		current.startRequest(agentlink.RawUSBImporter, importerToken, &device))
	cancel()
	if err != nil {
		return nil, err
	}
	importerStarted = true
	reserved = false
	return current, nil
}

func (reconciler *Reconciler) bindingStillCurrent(item desiredBinding) error {
	rawSnapshot, err := reconciler.catalog.RawModemBindings()
	if err != nil {
		return fmt.Errorf("re-read raw modem binding: %w", err)
	}
	found := false
	for _, binding := range rawSnapshot.Bindings {
		if binding.LineID == item.binding.LineID && sameBinding(binding, item.binding) {
			found = true
			break
		}
	}
	if !found || !item.binding.Enabled {
		return errors.New("raw modem binding changed during start")
	}
	line, err := reconciler.catalog.Get(item.line.ID)
	if err != nil {
		return fmt.Errorf("re-read raw modem line: %w", err)
	}
	if !line.Enabled || line.CardID != item.binding.CardID || line.SIM.IMEI != item.binding.EquipmentID {
		return errors.New("raw modem line identity changed during start")
	}
	return nil
}

func (reconciler *Reconciler) stop(ctx context.Context, current *session) error {
	if current == nil {
		return nil
	}
	actionContext, cancel := context.WithTimeout(ctx, reconciler.actionTimeout)
	_, importerErr := reconciler.agents.ExecuteRawUSB(actionContext,
		current.importerAgentID, current.importerProcessGeneration,
		current.stopRequest(agentlink.RawUSBImporter))
	cancel()
	if importerOwnsActiveModem(importerErr) {
		return importerErr
	}
	actionContext, cancel = context.WithTimeout(ctx, reconciler.actionTimeout)
	_, exporterErr := reconciler.agents.ExecuteRawUSB(actionContext,
		current.sourceAgentID, current.sourceProcessGeneration,
		current.stopRequest(agentlink.RawUSBExporter))
	cancel()
	reconciler.broker.Revoke(current.streamID)
	if cleanupErr := errors.Join(importerErr, exporterErr); cleanupErr != nil {
		// Revoking the paired WSS forces the source-side durable handoff record
		// into local VerifiedHangup recovery. Retaining an in-memory Core session
		// after that point would only block the level-triggered retry.
		reconciler.logf("raw modem line %s detached with endpoint errors: %v", current.lineID, cleanupErr)
	}
	return nil
}

func (reconciler *Reconciler) cleanupPartial(current *session, exporterStarted, importerStarted bool) {
	cleanupContext, cancel := context.WithTimeout(context.Background(), min(reconciler.actionTimeout, 5*time.Second))
	defer cancel()
	if importerStarted {
		_, _ = reconciler.agents.ExecuteRawUSB(cleanupContext,
			current.importerAgentID, current.importerProcessGeneration,
			current.stopRequest(agentlink.RawUSBImporter))
	}
	if exporterStarted {
		_, _ = reconciler.agents.ExecuteRawUSB(cleanupContext,
			current.sourceAgentID, current.sourceProcessGeneration,
			current.stopRequest(agentlink.RawUSBExporter))
	}
	reconciler.broker.Revoke(current.streamID)
}

func (reconciler *Reconciler) failed(state *lineState, now time.Time) {
	state.failures++
	delay := reconciler.baseBackoff
	for attempt := uint32(1); attempt < state.failures && delay < reconciler.maximumBackoff; attempt++ {
		delay *= 2
		if delay > reconciler.maximumBackoff {
			delay = reconciler.maximumBackoff
		}
	}
	state.retryAt = now.Add(delay)
}

func resolveSource(binding linecatalog.RawModemBinding, statuses []agentlink.ConnectionStatus,
	now time.Time, ttl time.Duration) (sourceTarget, error) {
	var result sourceTarget
	matches := 0
	for _, status := range statuses {
		if status.AgentID != binding.SourceAgentID || !fresh(status, now, ttl) ||
			status.Topology == nil || !status.Topology.RawUSBSource ||
			sourceSessionConflict(*status.Topology, binding) {
			continue
		}
		for _, recovery := range status.Topology.RawUSBRecoveries {
			if recovery.EquipmentID == binding.EquipmentID && recovery.CardID == binding.CardID &&
				recovery.State == "capture_reserved" {
				modem := agentlink.ModemFact{
					AttachmentID: recovery.AttachmentID, EquipmentID: recovery.EquipmentID,
					SIM: agentlink.ModemSIMFact{
						State: "ready", ICCID: recovery.CardID, SessionGeneration: recovery.SessionGeneration,
					},
				}
				result, matches = sourceTarget{status: status, modem: modem, recovering: true,
					captureGeneration: recovery.CaptureGeneration}, matches+1
			}
		}
	}
	if matches == 0 {
		return sourceTarget{}, errors.New("exact raw modem source is not ready")
	}
	if matches != 1 {
		return sourceTarget{}, errors.New("exact raw modem source is ambiguous")
	}
	return result, nil
}

func sourceSessionConflict(topology agentlink.TopologySnapshot, binding linecatalog.RawModemBinding) bool {
	for _, current := range topology.RawUSBSessions {
		if current.EquipmentID == binding.EquipmentID || current.CardID == binding.CardID {
			return true
		}
	}
	return false
}

func resolveImporter(importerID, sourceID string, statuses []agentlink.ConnectionStatus,
	now time.Time, ttl time.Duration) (agentlink.ConnectionStatus, error) {
	if importerID == sourceID {
		return agentlink.ConnectionStatus{}, errors.New("raw modem source and importer must be different Agents")
	}
	for _, status := range statuses {
		if status.AgentID == importerID && fresh(status, now, ttl) && status.Topology != nil && status.Topology.RawUSBImporter {
			return status, nil
		}
	}
	return agentlink.ConnectionStatus{}, errors.New("configured raw modem importer Agent is not ready")
}

func sessionReady(current *session, statuses []agentlink.ConnectionStatus, now time.Time, ttl time.Duration) bool {
	var sourceCapture, sourceSession, importerSession, importedModem bool
	for _, status := range statuses {
		if !fresh(status, now, ttl) || status.Topology == nil {
			continue
		}
		if status.AgentID == current.sourceAgentID && status.ProcessGeneration == current.sourceProcessGeneration {
			sourceSession = containsSession(*status.Topology, current, agentlink.RawUSBExporter)
			matches := 0
			for _, recovery := range status.Topology.RawUSBRecoveries {
				if recovery.EquipmentID == current.equipmentID && recovery.CardID == current.cardID &&
					recovery.CaptureGeneration == current.captureGeneration && recovery.State == "capture_reserved" {
					matches++
				}
			}
			sourceCapture = matches == 1
		}
		if status.AgentID == current.importerAgentID && status.ProcessGeneration == current.importerProcessGeneration {
			importerSession = containsSession(*status.Topology, current, agentlink.RawUSBImporter)
			if status.Topology.ModemCondition == agentlink.ModemReady {
				for _, modem := range status.Topology.Modems {
					if modem.EquipmentID == current.equipmentID && modem.SIM.ICCID == current.cardID &&
						modem.Condition == "ready" && modem.SIM.State == "ready" && modem.SIM.SessionGeneration != "" &&
						modem.AT.State == "ready" && modem.Network.DataGuard == "protected" &&
						modem.Network.Data == "disconnected" {
						if importedModem {
							return false
						}
						importedModem = true
					}
				}
			}
		}
	}
	return sourceCapture && sourceSession && importerSession && importedModem
}

func containsSession(topology agentlink.TopologySnapshot, current *session, role agentlink.RawUSBRole) bool {
	for _, fact := range topology.RawUSBSessions {
		if fact.Role == role && fact.SourceAgentID == current.sourceAgentID &&
			fact.SourceProcessGeneration == current.sourceProcessGeneration &&
			fact.AttachmentID == current.attachmentID && fact.SessionGeneration == current.sessionGeneration &&
			fact.EquipmentID == current.equipmentID && fact.CardID == current.cardID &&
			fact.USBSessionID == current.usbSessionID && fact.CaptureGeneration == current.captureGeneration &&
			fact.State == "transport_active" {
			return true
		}
	}
	return false
}

func fresh(status agentlink.ConnectionStatus, now time.Time, ttl time.Duration) bool {
	return !status.LastReport.IsZero() && !status.LastReport.Before(now.Add(-ttl))
}

func bindingKey(binding linecatalog.RawModemBinding) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", binding.Epoch, binding.SourceAgentID,
		binding.EquipmentID, binding.CardID, binding.ImporterAgentID)
}

func sameBinding(left, right linecatalog.RawModemBinding) bool {
	return left.SchemaVersion == right.SchemaVersion && left.Epoch == right.Epoch &&
		left.LineID == right.LineID && left.SourceAgentID == right.SourceAgentID &&
		left.EquipmentID == right.EquipmentID && left.CardID == right.CardID &&
		left.ImporterAgentID == right.ImporterAgentID && left.Enabled == right.Enabled
}

func (current *session) identity() agentusbip.SessionIdentity {
	return agentusbip.SessionIdentity{
		SourceAgentID: current.sourceAgentID, SourceProcessGeneration: current.sourceProcessGeneration,
		AttachmentID: current.attachmentID, SessionGeneration: current.sessionGeneration,
		EquipmentID: current.equipmentID, CardID: current.cardID,
		USBSessionID: current.usbSessionID, CaptureGeneration: current.captureGeneration,
		StreamID: current.streamID,
	}
}

func (current *session) startRequest(role agentlink.RawUSBRole, token string,
	device *agentlink.RawUSBDevice) agentlink.RawUSBRequest {
	action := agentlink.RawUSBExportStart
	if role == agentlink.RawUSBImporter {
		action = agentlink.RawUSBImportStart
	}
	return agentlink.RawUSBRequest{
		OperationID: "raw-start-" + current.usbSessionID, Action: action, Role: role,
		SourceAgentID: current.sourceAgentID, SourceProcessGeneration: current.sourceProcessGeneration,
		AttachmentID: current.attachmentID, SessionGeneration: current.sessionGeneration,
		EquipmentID: current.equipmentID, CardID: current.cardID,
		USBSessionID: current.usbSessionID, CaptureGeneration: current.captureGeneration,
		StreamID: current.streamID, StreamToken: token, Device: device,
		Recovering: role == agentlink.RawUSBExporter && current.recovering,
	}
}

func (current *session) stopRequest(role agentlink.RawUSBRole) agentlink.RawUSBRequest {
	return agentlink.RawUSBRequest{
		OperationID: "raw-stop-" + current.usbSessionID, Action: agentlink.RawUSBStop, Role: role,
		SourceAgentID: current.sourceAgentID, SourceProcessGeneration: current.sourceProcessGeneration,
		AttachmentID: current.attachmentID, SessionGeneration: current.sessionGeneration,
		EquipmentID: current.equipmentID, CardID: current.cardID, USBSessionID: current.usbSessionID,
		CaptureGeneration: current.captureGeneration,
		Recovering:        role == agentlink.RawUSBExporter && current.recovering,
	}
}

func importerOwnsActiveModem(err error) bool {
	var remote *agentlink.RemoteError
	return errors.As(err, &remote) &&
		(remote.Code == "raw_usb_paid_call_active" || remote.Code == "raw_usb_data_session_active")
}

func randomID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
