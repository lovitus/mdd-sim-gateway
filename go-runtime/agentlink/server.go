package agentlink

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	minimumTokenBytes = 32
	maximumMessage    = 64 << 10
	defaultPingEvery  = 10 * time.Second
	defaultPingWait   = 5 * time.Second
)

type TokenResolver interface {
	TokenForAgent(context.Context, string) (string, error)
}

type TokenResolverFunc func(context.Context, string) (string, error)

func (function TokenResolverFunc) TokenForAgent(ctx context.Context, agentID string) (string, error) {
	return function(ctx, agentID)
}

// ModemRouteAdmission constrains ordinary modem actions when another Core
// owner (for example a durable whole-Modem USB/IP binding) has selected one
// exact Agent. A constrained route must fail closed while that Agent/session
// is unavailable; callers may never fall back to another copy of the same
// equipment/card identity.
type ModemRouteAdmission interface {
	RequiredModemAgent(equipmentID, cardID string, statuses []ConnectionStatus) (agentID string, constrained bool, err error)
	RequiredCardAgent(cardID string, statuses []ConnectionStatus) (agentID string, constrained bool, err error)
}

var (
	ErrAgentOffline       = errors.New("Agent is offline")
	ErrGenerationMismatch = errors.New("Agent process generation does not match")
	ErrCardOffline        = errors.New("card is not present on an Agent")
	ErrCardAmbiguous      = errors.New("card identity is present on multiple Agents")
	ErrModemOffline       = errors.New("modem or SIM is not present on an Agent")
	ErrModemAmbiguous     = errors.New("modem and SIM identity is present on multiple Agents")
)

type Server struct {
	tokens         TokenResolver
	mu             sync.RWMutex
	agents         map[string]*serverConnection
	modemAdmission ModemRouteAdmission
	events         ModemEventSink
	nextID         atomic.Uint64
	pingEvery      time.Duration
	pingWait       time.Duration
}

type serverConnection struct {
	hello        Hello
	socket       *websocket.Conn
	closed       chan struct{}
	mu           sync.Mutex
	writeMu      sync.Mutex
	pending      map[string]chan envelope
	connectedAt  time.Time
	lastSeen     atomic.Int64
	healthMu     sync.RWMutex
	healthSeq    uint64
	capabilities []string
	lastReport   time.Time
	wireTopoRev  string
	topologyRev  string
	topology     *TopologySnapshot
}

type ConnectionStatus struct {
	AgentID           string            `json:"agent_id"`
	ProcessGeneration string            `json:"process_generation"`
	ConnectedAt       time.Time         `json:"connected_at"`
	LastSeen          time.Time         `json:"last_seen"`
	LastReport        time.Time         `json:"last_report,omitempty"`
	TopologyRevision  string            `json:"topology_revision,omitempty"`
	Topology          *TopologySnapshot `json:"topology,omitempty"`
	Capabilities      []string          `json:"capabilities,omitempty"`
}

type ModemTarget struct {
	AgentID           string
	ProcessGeneration string
	AttachmentID      string
	EquipmentID       string
	CardID            string
}

// CardRouteTarget is the current unique AKA-capable attachment for one ICCID.
// It grants no operation authority by itself: callers must resolve again at
// the paid or credential-bearing operation boundary.
type CardRouteTarget struct {
	AgentID           string
	ProcessGeneration string
	SessionGeneration string
	AttachmentID      string
	EquipmentID       string
	CardID            string
	Kind              string
}

type EUICCProfileTarget struct {
	AgentID           string
	ProcessGeneration string
	SessionGeneration string
}

type EUICCDownloadTarget struct {
	AgentID           string
	ProcessGeneration string
	SessionGeneration string
}

type EUICCDiscoveryTarget struct {
	AgentID           string
	ProcessGeneration string
	SessionGeneration string
}

type EUICCNotificationTarget struct {
	AgentID           string
	ProcessGeneration string
	SessionGeneration string
}

func (server *Server) Statuses() []ConnectionStatus {
	server.mu.RLock()
	ids := make([]string, 0, len(server.agents))
	for id := range server.agents {
		ids = append(ids, id)
	}
	server.mu.RUnlock()
	sort.Strings(ids)
	statuses := make([]ConnectionStatus, 0, len(ids))
	for _, id := range ids {
		if status, found := server.Status(id); found {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

func NewServer(tokens TokenResolver) (*Server, error) {
	if tokens == nil {
		return nil, errors.New("Agent token resolver is required")
	}
	return &Server{
		tokens: tokens, agents: make(map[string]*serverConnection),
		pingEvery: defaultPingEvery, pingWait: defaultPingWait,
	}, nil
}

// SetModemRouteAdmission installs the immutable Core-side modem route owner.
// It must be called before the first Agent connects.
func (server *Server) SetModemRouteAdmission(admission ModemRouteAdmission) error {
	if admission == nil {
		return errors.New("modem route admission is required")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.agents) != 0 || server.modemAdmission != nil {
		return errors.New("modem route admission cannot change after Agent service starts")
	}
	server.modemAdmission = admission
	return nil
}

// SetModemEventSink enables the additive Agent-originated event protocol. It
// must be installed before any Agent connects so the authenticated WebSocket
// upgrade header is a stable rolling-compatibility contract.
func (server *Server) SetModemEventSink(sink ModemEventSink) error {
	if sink == nil {
		return errors.New("modem event sink is required")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.agents) != 0 || server.events != nil {
		return errors.New("modem event sink cannot change after Agent service starts")
	}
	server.events = sink
	return nil
}

// ServeHTTP is mounted below the same HTTPS listener as browser/API routes.
// It accepts only an Agent-originated WebSocket and never opens a listener on
// the Agent host.
func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	agentID := strings.TrimSpace(request.Header.Get("X-MDD-Agent-ID"))
	if !validIdentifier(agentID) {
		http.Error(response, "invalid_agent", http.StatusBadRequest)
		return
	}
	token, err := server.tokens.TokenForAgent(request.Context(), agentID)
	if err != nil || len(token) < minimumTokenBytes || !bearerMatches(request.Header.Get("Authorization"), token) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	modemEventsCapable := featureEnabled(request.Header.Get(agentCapabilitiesHeader), modemEventsFeature)
	if server.events != nil && modemEventsCapable {
		response.Header().Set(agentFeaturesHeader, modemEventsFeature)
	}
	socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	socket.SetReadLimit(maximumMessage)
	defer socket.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	first, err := readEnvelope(ctx, socket)
	cancel()
	if err != nil || first.validate() != nil || first.Kind != kindHello || first.Hello.AgentID != agentID {
		_ = socket.Close(websocket.StatusPolicyViolation, "invalid hello")
		return
	}
	connection := &serverConnection{
		hello: *first.Hello, socket: socket, closed: make(chan struct{}),
		pending: make(map[string]chan envelope), connectedAt: time.Now(),
	}
	if modemEventsCapable {
		connection.capabilities = []string{modemEventsFeature}
	}
	connection.lastSeen.Store(connection.connectedAt.UnixNano())
	// Publish and acknowledge under the connection lock. AuthenticateAKA must
	// acquire this lock before it can enqueue/write a request, so hello_ack is
	// always the first server frame observed by the Agent.
	connection.mu.Lock()
	if !server.add(connection) {
		connection.mu.Unlock()
		_ = socket.Close(websocket.StatusPolicyViolation, "duplicate Agent")
		return
	}
	defer server.remove(connection)
	ackContext, cancelAck := context.WithTimeout(request.Context(), 10*time.Second)
	connection.writeMu.Lock()
	err = writeEnvelope(ackContext, socket, envelope{Kind: kindHelloAck})
	connection.writeMu.Unlock()
	cancelAck()
	connection.mu.Unlock()
	if err != nil {
		return
	}
	connectionContext, stopConnection := context.WithCancel(request.Context())
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		connection.keepalive(connectionContext, server.pingEvery, server.pingWait)
	}()
	server.readLoop(connectionContext, connection)
	stopConnection()
	<-pingDone
}

func (server *Server) Status(agentID string) (ConnectionStatus, bool) {
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return ConnectionStatus{}, false
	}
	connection.healthMu.RLock()
	lastReport := connection.lastReport
	topologyRevision := connection.topologyRev
	topology := cloneTopology(connection.topology)
	connection.healthMu.RUnlock()
	return ConnectionStatus{
		AgentID: connection.hello.AgentID, ProcessGeneration: connection.hello.ProcessGeneration,
		ConnectedAt: connection.connectedAt, LastSeen: time.Unix(0, connection.lastSeen.Load()),
		LastReport: lastReport, TopologyRevision: topologyRevision, Topology: topology,
		Capabilities: append([]string(nil), connection.capabilities...),
	}, true
}

func (server *Server) AuthenticateAKA(ctx context.Context, agentID, processGeneration string, request AKARequest) (AKAResponse, error) {
	if err := request.Validate(); err != nil {
		return AKAResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return AKAResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != processGeneration {
		return AKAResponse{}, ErrGenerationMismatch
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindAKARequest, AKARequest: &request})
	if err != nil {
		return AKAResponse{}, err
	}
	if message.AKAResult == nil {
		return AKAResponse{}, errors.New("Agent returned an empty AKA response")
	}
	if err := message.AKAResult.ValidateFor(request); err != nil {
		return AKAResponse{}, err
	}
	if message.AKAResult.Failure != nil {
		return *message.AKAResult, message.AKAResult.Failure
	}
	return *message.AKAResult, nil
}

func (server *Server) ExecuteEUICCProfile(ctx context.Context, agentID, processGeneration string,
	request EUICCProfileRequest) (EUICCProfileResponse, error) {
	if err := request.Validate(); err != nil {
		return EUICCProfileResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return EUICCProfileResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != processGeneration {
		return EUICCProfileResponse{}, ErrGenerationMismatch
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindEUICCRequest, EUICCRequest: &request})
	if err != nil {
		return EUICCProfileResponse{}, err
	}
	if message.EUICCResult == nil {
		return EUICCProfileResponse{}, errors.New("Agent returned an empty eUICC profile response")
	}
	if err := message.EUICCResult.ValidateFor(request); err != nil {
		return EUICCProfileResponse{}, err
	}
	if message.EUICCResult.Failure != nil {
		return *message.EUICCResult, message.EUICCResult.Failure
	}
	return *message.EUICCResult, nil
}

// ExecuteEUICCProfileCommand resolves EID+ICCID to one current insertion and
// never falls back to a reader name, first attachment, or another Agent.
func (server *Server) ExecuteEUICCProfileCommand(ctx context.Context,
	command EUICCProfileCommand) (EUICCProfileResponse, error) {
	if err := command.Validate(); err != nil {
		return EUICCProfileResponse{}, err
	}
	selected, err := server.ResolveEUICCProfileTarget(command.EID, command.ICCID)
	if err != nil {
		return EUICCProfileResponse{}, err
	}
	return server.ExecuteEUICCProfile(ctx, selected.AgentID, selected.ProcessGeneration,
		command.requestFor(selected.SessionGeneration))
}

func (server *Server) ResolveEUICCProfileTarget(eid, iccid string) (EUICCProfileTarget, error) {
	if !validEID(eid) || !validCardID(iccid) {
		return EUICCProfileTarget{}, errors.New("invalid eUICC profile target")
	}
	var matches []EUICCProfileTarget
	for _, status := range server.Statuses() {
		if status.Topology == nil || status.Topology.ReaderCondition != ReaderReady {
			continue
		}
		for _, reader := range status.Topology.Readers {
			if reader.IdentityState != CardIdentified {
				continue
			}
			for _, slot := range ReaderEUICCs(reader) {
				if slot.EUICC.EID != eid || !slot.EUICC.ProfilesAvailable || !slot.EUICC.ProfileManagement {
					continue
				}
				for _, profile := range slot.EUICC.Profiles {
					if profile.ICCID == iccid {
						matches = append(matches, EUICCProfileTarget{
							AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
							SessionGeneration: reader.SessionGeneration,
						})
					}
				}
			}
		}
	}
	if len(matches) == 0 {
		return EUICCProfileTarget{}, ErrCardOffline
	}
	if len(matches) != 1 {
		return EUICCProfileTarget{}, ErrCardAmbiguous
	}
	return matches[0], nil
}

func (server *Server) ExecuteEUICCDownload(ctx context.Context, agentID, processGeneration string,
	request EUICCDownloadRequest) (EUICCDownloadResponse, error) {
	if err := request.Validate(); err != nil {
		return EUICCDownloadResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return EUICCDownloadResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != processGeneration {
		return EUICCDownloadResponse{}, ErrGenerationMismatch
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindDownloadRequest, DownloadRequest: &request})
	if err != nil {
		return EUICCDownloadResponse{}, err
	}
	if message.DownloadResult == nil {
		return EUICCDownloadResponse{}, errors.New("Agent returned an empty eUICC download response")
	}
	if err := message.DownloadResult.ValidateFor(request); err != nil {
		return EUICCDownloadResponse{}, err
	}
	if message.DownloadResult.Failure != nil {
		return *message.DownloadResult, message.DownloadResult.Failure
	}
	return *message.DownloadResult, nil
}

func (server *Server) ExecuteEUICCDownloadCommand(ctx context.Context,
	command EUICCDownloadCommand) (EUICCDownloadResponse, error) {
	if err := command.Validate(); err != nil {
		return EUICCDownloadResponse{}, err
	}
	selected, err := server.ResolveEUICCDownloadTarget(command.EID)
	if err != nil {
		return EUICCDownloadResponse{}, err
	}
	return server.ExecuteEUICCDownload(ctx, selected.AgentID, selected.ProcessGeneration,
		command.requestFor(selected.SessionGeneration))
}

// ResolveEUICCDownloadTarget permits blank eUICCs while still requiring one
// exact live EID and an Agent that explicitly advertises download support.
func (server *Server) ResolveEUICCDownloadTarget(eid string) (EUICCDownloadTarget, error) {
	if !validEID(eid) {
		return EUICCDownloadTarget{}, errors.New("invalid eUICC download target")
	}
	var matches []EUICCDownloadTarget
	for _, status := range server.Statuses() {
		if status.Topology == nil || status.Topology.ReaderCondition != ReaderReady {
			continue
		}
		for _, reader := range status.Topology.Readers {
			if reader.IdentityState != CardIdentified {
				continue
			}
			for _, slot := range ReaderEUICCs(reader) {
				if slot.EUICC.EID == eid && slot.EUICC.ProfileDownload {
					matches = append(matches, EUICCDownloadTarget{
						AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
						SessionGeneration: reader.SessionGeneration,
					})
				}
			}
		}
	}
	if len(matches) == 0 {
		return EUICCDownloadTarget{}, ErrCardOffline
	}
	if len(matches) != 1 {
		return EUICCDownloadTarget{}, ErrCardAmbiguous
	}
	return matches[0], nil
}

func (server *Server) ExecuteEUICCDiscovery(ctx context.Context, agentID, processGeneration string,
	request EUICCDiscoveryRequest) (EUICCDiscoveryResponse, error) {
	if err := request.Validate(); err != nil {
		return EUICCDiscoveryResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return EUICCDiscoveryResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != processGeneration {
		return EUICCDiscoveryResponse{}, ErrGenerationMismatch
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindDiscoveryRequest, DiscoveryRequest: &request})
	if err != nil {
		return EUICCDiscoveryResponse{}, err
	}
	if message.DiscoveryResult == nil {
		return EUICCDiscoveryResponse{}, errors.New("Agent returned an empty eUICC discovery response")
	}
	if err := message.DiscoveryResult.ValidateFor(request); err != nil {
		return EUICCDiscoveryResponse{}, err
	}
	if message.DiscoveryResult.Failure != nil {
		return *message.DiscoveryResult, message.DiscoveryResult.Failure
	}
	return *message.DiscoveryResult, nil
}

func (server *Server) ExecuteEUICCDiscoveryCommand(ctx context.Context,
	command EUICCDiscoveryCommand) (EUICCDiscoveryResponse, error) {
	if err := command.Validate(); err != nil {
		return EUICCDiscoveryResponse{}, err
	}
	selected, err := server.ResolveEUICCDiscoveryTarget(command.EID)
	if err != nil {
		return EUICCDiscoveryResponse{}, err
	}
	return server.ExecuteEUICCDiscovery(ctx, selected.AgentID, selected.ProcessGeneration,
		command.requestFor(selected.SessionGeneration))
}

func (server *Server) ResolveEUICCDiscoveryTarget(eid string) (EUICCDiscoveryTarget, error) {
	if !validEID(eid) {
		return EUICCDiscoveryTarget{}, errors.New("invalid eUICC discovery target")
	}
	var matches []EUICCDiscoveryTarget
	for _, status := range server.Statuses() {
		if status.Topology == nil || status.Topology.ReaderCondition != ReaderReady {
			continue
		}
		for _, reader := range status.Topology.Readers {
			if reader.IdentityState != CardIdentified {
				continue
			}
			for _, slot := range ReaderEUICCs(reader) {
				if slot.EUICC.EID == eid && slot.EUICC.ProfileDiscovery {
					matches = append(matches, EUICCDiscoveryTarget{
						AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
						SessionGeneration: reader.SessionGeneration,
					})
				}
			}
		}
	}
	if len(matches) == 0 {
		return EUICCDiscoveryTarget{}, ErrCardOffline
	}
	if len(matches) != 1 {
		return EUICCDiscoveryTarget{}, ErrCardAmbiguous
	}
	return matches[0], nil
}

func (server *Server) ExecuteEUICCNotification(ctx context.Context, agentID, processGeneration string,
	request EUICCNotificationRequest) (EUICCNotificationResponse, error) {
	if err := request.Validate(); err != nil {
		return EUICCNotificationResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return EUICCNotificationResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != processGeneration {
		return EUICCNotificationResponse{}, ErrGenerationMismatch
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindNotificationRequest, NotificationRequest: &request})
	if err != nil {
		return EUICCNotificationResponse{}, err
	}
	if message.NotificationResult == nil {
		return EUICCNotificationResponse{}, errors.New("Agent returned an empty eUICC notification response")
	}
	if err := message.NotificationResult.ValidateFor(request); err != nil {
		return EUICCNotificationResponse{}, err
	}
	if message.NotificationResult.Failure != nil {
		return *message.NotificationResult, message.NotificationResult.Failure
	}
	return *message.NotificationResult, nil
}

func (server *Server) ExecuteEUICCNotificationCommand(ctx context.Context,
	command EUICCNotificationCommand) (EUICCNotificationResponse, error) {
	if err := command.Validate(); err != nil {
		return EUICCNotificationResponse{}, err
	}
	selected, err := server.resolveEUICCNotificationTarget(command.EID, command.Action)
	if err != nil {
		return EUICCNotificationResponse{}, err
	}
	return server.ExecuteEUICCNotification(ctx, selected.AgentID, selected.ProcessGeneration,
		command.requestFor(selected.SessionGeneration))
}

func (server *Server) ResolveEUICCNotificationTarget(eid string) (EUICCNotificationTarget, error) {
	return server.resolveEUICCNotificationTarget(eid, "")
}

func (server *Server) resolveEUICCNotificationTarget(eid string, action EUICCNotificationAction) (EUICCNotificationTarget, error) {
	if !validEID(eid) {
		return EUICCNotificationTarget{}, errors.New("invalid eUICC notification target")
	}
	var matches []EUICCNotificationTarget
	for _, status := range server.Statuses() {
		if status.Topology == nil || status.Topology.ReaderCondition != ReaderReady {
			continue
		}
		for _, reader := range status.Topology.Readers {
			if reader.IdentityState != CardIdentified {
				continue
			}
			for _, slot := range ReaderEUICCs(reader) {
				capable := slot.EUICC.NotificationInventory
				if action == EUICCNotificationDeliver {
					capable = slot.EUICC.NotificationDelivery
				} else if action == EUICCNotificationRemove {
					capable = slot.EUICC.NotificationRemoval
				}
				if slot.EUICC.EID == eid && capable {
					matches = append(matches, EUICCNotificationTarget{
						AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
						SessionGeneration: reader.SessionGeneration,
					})
				}
			}
		}
	}
	if len(matches) == 0 {
		return EUICCNotificationTarget{}, ErrCardOffline
	}
	if len(matches) != 1 {
		return EUICCNotificationTarget{}, ErrCardAmbiguous
	}
	return matches[0], nil
}

func (server *Server) ExecuteModem(ctx context.Context, agentID, processGeneration string, request ModemRequest) (ModemResponse, error) {
	if err := request.Validate(); err != nil {
		return ModemResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return ModemResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != processGeneration {
		return ModemResponse{}, ErrGenerationMismatch
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindModemRequest, ModemRequest: &request})
	if err != nil {
		return ModemResponse{}, err
	}
	if message.ModemResult == nil {
		return ModemResponse{}, errors.New("Agent returned an empty modem response")
	}
	if err := message.ModemResult.ValidateFor(request); err != nil {
		return ModemResponse{}, err
	}
	if message.ModemResult.Failure != nil {
		return *message.ModemResult, message.ModemResult.Failure
	}
	return *message.ModemResult, nil
}

// ExecuteModemCommand resolves stable equipment and SIM identities to one
// current Agent process/attachment, then sends the exact fenced request over
// that Agent's existing WSS connection.
func (server *Server) ExecuteModemCommand(ctx context.Context, command ModemCommand) (ModemResponse, error) {
	if err := command.Validate(); err != nil {
		return ModemResponse{}, err
	}
	selected, err := server.ResolveModemTargetForAction(command.EquipmentID, command.CardID, command.Action)
	if err != nil {
		return ModemResponse{}, err
	}
	return server.ExecuteModem(ctx, selected.AgentID, selected.ProcessGeneration,
		command.requestFor(selected.AttachmentID))
}

func (server *Server) ResolveModemTarget(equipmentID, cardID string) (ModemTarget, error) {
	return server.ResolveModemTargetForAction(equipmentID, cardID, ModemCallStatus)
}

// ResolveModemTargetForCardAction selects the current physical modem from the
// SIM identity alone. This is the adapted-mode route: presentation IMEI is a
// Provider identity and must never pin a SIM to one old modem. The returned
// target still contains the actual equipment, Agent process and attachment
// fences used by the operation request.
func (server *Server) ResolveModemTargetForCardAction(cardID string, action ModemAction) (ModemTarget, error) {
	return server.resolveModemTarget("", cardID, action, false)
}

// ResolveModemTargetForAction selects one current attachment that advertises
// the specific typed capability. SMS must not depend on voice readiness, and
// call/media must not be inferred from SMS readiness.
func (server *Server) ResolveModemTargetForAction(equipmentID, cardID string, action ModemAction) (ModemTarget, error) {
	return server.resolveModemTarget(equipmentID, cardID, action, true)
}

func (server *Server) resolveModemTarget(equipmentID, cardID string, action ModemAction,
	exactEquipment bool) (ModemTarget, error) {
	if (exactEquipment && !validEquipmentID(equipmentID)) || !validCardID(cardID) {
		return ModemTarget{}, errors.New("invalid modem target identity")
	}
	if !validModemAction(action) {
		return ModemTarget{}, errors.New("invalid modem target action")
	}
	requiresSMS := action == ModemSMSList || action == ModemSMSSend
	statuses := server.Statuses()
	var requiredAgent string
	var constrained bool
	var err error
	if exactEquipment {
		requiredAgent, constrained, err = server.requiredModemAgent(equipmentID, cardID, statuses)
	} else {
		requiredAgent, constrained, err = server.requiredCardAgent(cardID, statuses)
		if errors.Is(err, ErrCardOffline) {
			err = ErrModemOffline
		}
	}
	if err != nil {
		return ModemTarget{}, err
	}
	if !exactEquipment {
		switch countCardOccurrences(statuses, cardID) {
		case 0:
			return ModemTarget{}, ErrModemOffline
		case 1:
		default:
			return ModemTarget{}, ErrModemAmbiguous
		}
	}
	var matches []ModemTarget
	for _, status := range statuses {
		if constrained && status.AgentID != requiredAgent {
			continue
		}
		if status.Topology == nil || status.Topology.ModemCondition != ModemReady {
			continue
		}
		for _, modem := range status.Topology.Modems {
			adaptedReady := exactEquipment || modem.Condition == "ready" && modem.SIM.State == "ready" &&
				modem.SIM.SessionGeneration != ""
			if (!exactEquipment || modem.EquipmentID == equipmentID) && adaptedReady && modem.SIM.ICCID == cardID &&
				modem.AT.State == "ready" &&
				(requiresSMS && modem.AT.SMS || !requiresSMS && modem.AT.CallSignalling) {
				matches = append(matches, ModemTarget{
					AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
					AttachmentID: modem.AttachmentID, EquipmentID: modem.EquipmentID, CardID: cardID,
				})
			}
		}
	}
	if len(matches) == 0 {
		return ModemTarget{}, ErrModemOffline
	}
	if len(matches) != 1 {
		return ModemTarget{}, ErrModemAmbiguous
	}
	return matches[0], nil
}

func (server *Server) ExecuteModemMedia(ctx context.Context, agentID, processGeneration string, request ModemMediaRequest) (ModemMediaResponse, error) {
	if err := request.Validate(); err != nil {
		return ModemMediaResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return ModemMediaResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != processGeneration {
		return ModemMediaResponse{}, ErrGenerationMismatch
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindMediaRequest, MediaRequest: &request})
	if err != nil {
		return ModemMediaResponse{}, err
	}
	if message.MediaResult == nil {
		return ModemMediaResponse{}, errors.New("Agent returned an empty modem media response")
	}
	if err := message.MediaResult.ValidateFor(request); err != nil {
		return ModemMediaResponse{}, err
	}
	if message.MediaResult.Failure != nil {
		return *message.MediaResult, message.MediaResult.Failure
	}
	return *message.MediaResult, nil
}

// ExecuteModemMediaCommand resolves the same exact current attachment as call
// signalling. It never falls back to another modem, SIM, or Agent generation.
func (server *Server) ExecuteModemMediaCommand(ctx context.Context, command ModemMediaCommand) (ModemMediaResponse, error) {
	if err := command.Validate(); err != nil {
		return ModemMediaResponse{}, err
	}
	selected, err := server.ResolveModemTarget(command.EquipmentID, command.CardID)
	if err != nil {
		return ModemMediaResponse{}, err
	}
	return server.ExecuteModemMedia(ctx, selected.AgentID, selected.ProcessGeneration,
		command.requestFor(selected.AttachmentID))
}

// ResolveModemDataTarget deliberately depends on data capability and the
// persistent guard, not AT voice/SMS readiness. Borrowing remains unavailable
// unless the exact current attachment is already fail-closed by the Agent.
func (server *Server) ResolveModemDataTarget(equipmentID, cardID string) (ModemTarget, error) {
	return server.resolveModemDataTarget(equipmentID, cardID, true)
}

// ResolveModemDataTargetForCard is the adapted-mode data route. It is
// deliberately independent of voice and SMS capabilities while retaining the
// same global card-identity and raw-binding admission fences.
func (server *Server) ResolveModemDataTargetForCard(cardID string) (ModemTarget, error) {
	return server.resolveModemDataTarget("", cardID, false)
}

func (server *Server) resolveModemDataTarget(equipmentID, cardID string, exactEquipment bool) (ModemTarget, error) {
	if (exactEquipment && !validEquipmentID(equipmentID)) || !validCardID(cardID) {
		return ModemTarget{}, errors.New("invalid modem data target identity")
	}
	statuses := server.Statuses()
	var requiredAgent string
	var constrained bool
	var err error
	if exactEquipment {
		requiredAgent, constrained, err = server.requiredModemAgent(equipmentID, cardID, statuses)
	} else {
		requiredAgent, constrained, err = server.requiredCardAgent(cardID, statuses)
		if errors.Is(err, ErrCardOffline) {
			err = ErrModemOffline
		}
	}
	if err != nil {
		return ModemTarget{}, err
	}
	if !exactEquipment {
		switch countCardOccurrences(statuses, cardID) {
		case 0:
			return ModemTarget{}, ErrModemOffline
		case 1:
		default:
			return ModemTarget{}, ErrModemAmbiguous
		}
	}
	var matches []ModemTarget
	for _, status := range statuses {
		if constrained && status.AgentID != requiredAgent {
			continue
		}
		if status.Topology == nil || status.Topology.ModemCondition != ModemReady {
			continue
		}
		for _, modem := range status.Topology.Modems {
			adaptedReady := exactEquipment || modem.Condition == "ready" && modem.SIM.SessionGeneration != "" &&
				modem.AT.State == "ready"
			if (!exactEquipment || modem.EquipmentID == equipmentID) && adaptedReady &&
				modem.SIM.State == "ready" && modem.SIM.ICCID == cardID && modem.Capabilities.CellularData &&
				modem.Network.DataGuard == "protected" {
				matches = append(matches, ModemTarget{
					AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
					AttachmentID: modem.AttachmentID, EquipmentID: modem.EquipmentID, CardID: cardID,
				})
			}
		}
	}
	if len(matches) == 0 {
		return ModemTarget{}, ErrModemOffline
	}
	if len(matches) != 1 {
		return ModemTarget{}, ErrModemAmbiguous
	}
	return matches[0], nil
}

// countCardOccurrences counts physical card endpoints, not capabilities. A
// second reader or modem carrying the same ICCID makes every adapted paid
// operation ambiguous even when that second endpoint cannot perform the
// requested action. For multi-SE readers each secure element is one endpoint;
// the reader-level CardID is ignored when explicit SE slots are present.
func countCardOccurrences(statuses []ConnectionStatus, cardID string) int {
	count := 0
	for _, status := range statuses {
		if status.Topology == nil {
			continue
		}
		for _, reader := range status.Topology.Readers {
			if !reader.CardPresent || reader.IdentityState != CardIdentified {
				continue
			}
			slots := ReaderEUICCs(reader)
			if len(slots) == 0 {
				if reader.CardID == cardID {
					count++
				}
				continue
			}
			for _, slot := range slots {
				matched := false
				for _, profile := range slot.EUICC.Profiles {
					if profile.State == EUICCProfileEnabled && profile.ICCID == cardID {
						matched = true
						break
					}
				}
				if matched {
					count++
				}
			}
		}
		for _, modem := range status.Topology.Modems {
			if modem.SIM.State == "ready" && modem.SIM.ICCID == cardID && modem.SIM.SessionGeneration != "" {
				count++
			}
		}
	}
	return count
}

func (server *Server) requiredModemAgent(equipmentID, cardID string,
	statuses []ConnectionStatus) (string, bool, error) {
	server.mu.RLock()
	admission := server.modemAdmission
	server.mu.RUnlock()
	if admission == nil {
		return "", false, nil
	}
	agentID, constrained, err := admission.RequiredModemAgent(equipmentID, cardID, statuses)
	if err != nil {
		return "", constrained, err
	}
	if constrained && !validIdentifier(agentID) {
		return "", true, ErrModemOffline
	}
	return agentID, constrained, nil
}

func (server *Server) requiredCardAgent(cardID string, statuses []ConnectionStatus) (string, bool, error) {
	server.mu.RLock()
	admission := server.modemAdmission
	server.mu.RUnlock()
	if admission == nil {
		return "", false, nil
	}
	agentID, constrained, err := admission.RequiredCardAgent(cardID, statuses)
	if err != nil {
		return "", constrained, err
	}
	if constrained && !validIdentifier(agentID) {
		return "", true, ErrCardOffline
	}
	return agentID, constrained, nil
}

func (server *Server) ExecuteModemData(ctx context.Context, agentID, processGeneration string, request ModemDataRequest) (ModemDataResponse, error) {
	if err := request.Validate(); err != nil {
		return ModemDataResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return ModemDataResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != processGeneration {
		return ModemDataResponse{}, ErrGenerationMismatch
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindDataRequest, DataRequest: &request})
	if err != nil {
		return ModemDataResponse{}, err
	}
	if message.DataResult == nil {
		return ModemDataResponse{}, errors.New("Agent returned an empty modem data response")
	}
	if err := message.DataResult.ValidateFor(request); err != nil {
		return ModemDataResponse{}, err
	}
	if message.DataResult.Failure != nil {
		return *message.DataResult, message.DataResult.Failure
	}
	return *message.DataResult, nil
}

func (server *Server) ExecuteModemDataCommand(ctx context.Context, command ModemDataCommand) (ModemDataResponse, error) {
	if err := command.Validate(); err != nil {
		return ModemDataResponse{}, err
	}
	selected, err := server.ResolveModemDataTarget(command.EquipmentID, command.CardID)
	if err != nil {
		return ModemDataResponse{}, err
	}
	return server.ExecuteModemData(ctx, selected.AgentID, selected.ProcessGeneration,
		command.requestFor(selected.AttachmentID))
}

// ExecuteRawUSB sends one already resolved whole-modem transport action to an
// exact Agent process. Raw mode is intentionally not auto-resolved by ICCID:
// Core's persistent raw binding must select both source and importer Agents.
func (server *Server) ExecuteRawUSB(ctx context.Context, agentID, processGeneration string, request RawUSBRequest) (RawUSBResponse, error) {
	if err := request.Validate(); err != nil {
		return RawUSBResponse{}, err
	}
	server.mu.RLock()
	connection := server.agents[agentID]
	server.mu.RUnlock()
	if connection == nil {
		return RawUSBResponse{}, ErrAgentOffline
	}
	if connection.hello.ProcessGeneration != processGeneration {
		return RawUSBResponse{}, ErrGenerationMismatch
	}
	message, err := server.roundTrip(ctx, connection, envelope{Kind: kindRawUSBRequest, RawUSBRequest: &request})
	if err != nil {
		return RawUSBResponse{}, err
	}
	if message.RawUSBResult == nil {
		return RawUSBResponse{}, errors.New("Agent returned an empty raw USB response")
	}
	if err := message.RawUSBResult.ValidateFor(request); err != nil {
		return RawUSBResponse{}, err
	}
	if message.RawUSBResult.Failure != nil {
		return *message.RawUSBResult, message.RawUSBResult.Failure
	}
	return *message.RawUSBResult, nil
}

func (server *Server) roundTrip(ctx context.Context, connection *serverConnection, message envelope) (envelope, error) {
	requestID := fmt.Sprintf("req-%d", server.nextID.Add(1))
	reply := make(chan envelope, 1)
	connection.mu.Lock()
	select {
	case <-connection.closed:
		connection.mu.Unlock()
		return envelope{}, ErrAgentOffline
	default:
	}
	connection.pending[requestID] = reply
	connection.mu.Unlock()
	defer connection.deletePending(requestID)
	message.RequestID = requestID
	connection.writeMu.Lock()
	err := writeEnvelope(ctx, connection.socket, message)
	connection.writeMu.Unlock()
	if err != nil {
		return envelope{}, fmt.Errorf("send Agent request: %w", err)
	}
	select {
	case <-ctx.Done():
		return envelope{}, ctx.Err()
	case <-connection.closed:
		return envelope{}, ErrAgentOffline
	case response := <-reply:
		return response, nil
	}
}

// AuthenticateCardAKA resolves a stable ICCID to one current Agent attachment,
// then delegates to the exact process/session fenced operation. A topology
// change between resolution and execution is rejected by the existing Agent
// generation checks; it is never redirected to a different card.
func (server *Server) AuthenticateCardAKA(ctx context.Context, challenge AKAChallenge) (AKAResponse, error) {
	if err := challenge.Validate(); err != nil {
		return AKAResponse{}, err
	}
	selected, err := server.ResolveCardRoute(challenge.CardID)
	if err != nil {
		return AKAResponse{}, err
	}
	request := challenge.requestFor(selected.SessionGeneration)
	if selected.Kind == "modem" {
		request = challenge.requestForModem(selected.SessionGeneration, selected.AttachmentID, selected.EquipmentID)
	}
	return server.AuthenticateAKA(ctx, selected.AgentID, selected.ProcessGeneration, request)
}

// ResolveCardRoute applies the same live Agent, generation, attachment and raw
// admission rules as AuthenticateCardAKA without sending a challenge. It is
// used to fence paid VoWiFi actions to the card that the browser selected.
func (server *Server) ResolveCardRoute(cardID string) (CardRouteTarget, error) {
	if !validCardID(cardID) {
		return CardRouteTarget{}, errors.New("invalid card route identity")
	}
	statuses := server.Statuses()
	requiredAgent, constrained, err := server.requiredCardAgent(cardID, statuses)
	if err != nil {
		return CardRouteTarget{}, err
	}
	var matches []CardRouteTarget
	for _, status := range statuses {
		if constrained && status.AgentID != requiredAgent {
			continue
		}
		if status.Topology == nil {
			continue
		}
		if status.Topology.ReaderCondition == ReaderReady {
			for _, reader := range status.Topology.Readers {
				if reader.IdentityState == CardIdentified && reader.CardID == cardID {
					matches = append(matches, CardRouteTarget{
						AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
						SessionGeneration: reader.SessionGeneration, CardID: cardID, Kind: "reader",
					})
				}
			}
		}
		if status.Topology.ModemCondition == ModemReady {
			for _, modem := range status.Topology.Modems {
				if modem.AT.State == "ready" && modem.AT.SIMAPDU &&
					modem.SIM.State == "ready" && modem.SIM.ICCID == cardID {
					matches = append(matches, CardRouteTarget{
						AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
						SessionGeneration: modem.SIM.SessionGeneration, AttachmentID: modem.AttachmentID,
						EquipmentID: modem.EquipmentID, CardID: cardID, Kind: "modem",
					})
				}
			}
		}
	}
	if len(matches) == 0 {
		return CardRouteTarget{}, ErrCardOffline
	}
	if len(matches) != 1 {
		return CardRouteTarget{}, ErrCardAmbiguous
	}
	return matches[0], nil
}

func (server *Server) add(connection *serverConnection) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	if _, exists := server.agents[connection.hello.AgentID]; exists {
		return false
	}
	server.agents[connection.hello.AgentID] = connection
	return true
}

func (server *Server) remove(connection *serverConnection) {
	server.mu.Lock()
	if server.agents[connection.hello.AgentID] == connection {
		delete(server.agents, connection.hello.AgentID)
	}
	server.mu.Unlock()
	connection.mu.Lock()
	select {
	case <-connection.closed:
	default:
		close(connection.closed)
	}
	connection.mu.Unlock()
}

func (server *Server) readLoop(ctx context.Context, connection *serverConnection) {
	for {
		message, err := readEnvelope(ctx, connection.socket)
		if err != nil {
			return
		}
		if message.validate() != nil {
			_ = connection.socket.Close(websocket.StatusPolicyViolation, "invalid response")
			return
		}
		if message.Kind == kindHealth {
			if err := connection.applyHealth(*message.Health); err != nil {
				_ = connection.socket.Close(websocket.StatusPolicyViolation, "invalid health")
				return
			}
			connection.lastSeen.Store(time.Now().UnixNano())
			continue
		}
		if message.Kind == kindModemEvent {
			if server.events == nil || !featureEnabled(strings.Join(connection.capabilities, ","), modemEventsFeature) {
				_ = connection.socket.Close(websocket.StatusPolicyViolation, "modem events unavailable")
				return
			}
			disposition := server.events.AcceptModemEvent(ctx, AgentEventContext{
				AgentID: connection.hello.AgentID, ProcessGeneration: connection.hello.ProcessGeneration,
			}, *message.ModemEvent)
			ack := ModemEventAck{EventID: message.ModemEvent.EventID, Accepted: disposition.Accepted,
				Retryable: disposition.Retryable, Code: disposition.Code}
			if ack.Validate() != nil {
				_ = connection.socket.Close(websocket.StatusInternalError, "invalid modem event disposition")
				return
			}
			ackContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			connection.writeMu.Lock()
			err = writeEnvelope(ackContext, connection.socket, envelope{Kind: kindModemEventAck, ModemEventAck: &ack})
			connection.writeMu.Unlock()
			cancel()
			if err != nil {
				return
			}
			connection.lastSeen.Store(time.Now().UnixNano())
			continue
		}
		if message.Kind != kindAKAResponse && message.Kind != kindModemResponse && message.Kind != kindMediaResponse && message.Kind != kindDataResponse && message.Kind != kindRawUSBResponse &&
			message.Kind != kindEUICCResponse && message.Kind != kindDownloadResponse && message.Kind != kindDiscoveryResponse &&
			message.Kind != kindNotificationResponse {
			_ = connection.socket.Close(websocket.StatusPolicyViolation, "invalid response")
			return
		}
		connection.mu.Lock()
		reply := connection.pending[message.RequestID]
		connection.mu.Unlock()
		if reply == nil {
			// A bounded Core operation may time out before a non-cancelable native
			// card call returns. Its late response has no pending consumer and must
			// not tear down unrelated Agent health or operations.
			continue
		}
		select {
		case reply <- message:
			connection.lastSeen.Store(time.Now().UnixNano())
		default:
			// Duplicate delivery is inert: only the first exact response can win.
			continue
		}
	}
}

func (connection *serverConnection) applyHealth(report HealthReport) error {
	connection.healthMu.Lock()
	defer connection.healthMu.Unlock()
	if report.Sequence <= connection.healthSeq {
		return errors.New("Agent health sequence did not increase")
	}
	if report.Topology == nil {
		if connection.healthSeq == 0 || report.TopologyRevision != connection.wireTopoRev {
			return errors.New("Agent health heartbeat has no matching topology")
		}
	} else {
		canonicalRevision, err := report.Topology.Revision()
		if err != nil {
			return err
		}
		connection.topology = cloneTopology(report.Topology)
		// The authenticated Agent's wire revision is valid only for deciding
		// whether its later lightweight heartbeat refers to the same payload.
		// Core publishes its own canonical revision so additive struct fields do
		// not make a Core-first rolling upgrade reject an older Agent.
		connection.wireTopoRev = report.TopologyRevision
		connection.topologyRev = canonicalRevision
	}
	connection.healthSeq = report.Sequence
	connection.lastReport = time.Now()
	return nil
}

func cloneTopology(source *TopologySnapshot) *TopologySnapshot {
	if source == nil {
		return nil
	}
	copy := NormalizeTopology(*source)
	return &copy
}

func (connection *serverConnection) keepalive(ctx context.Context, every, wait time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingContext, cancel := context.WithTimeout(ctx, wait)
			err := connection.socket.Ping(pingContext)
			cancel()
			if err != nil {
				connection.socket.CloseNow()
				return
			}
			connection.lastSeen.Store(time.Now().UnixNano())
		}
	}
}

func (connection *serverConnection) deletePending(requestID string) {
	connection.mu.Lock()
	delete(connection.pending, requestID)
	connection.mu.Unlock()
}

func bearerMatches(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	expected := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(presented[:], expected[:]) == 1
}
