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

var (
	ErrAgentOffline       = errors.New("Agent is offline")
	ErrGenerationMismatch = errors.New("Agent process generation does not match")
	ErrCardOffline        = errors.New("card is not present on an Agent")
	ErrCardAmbiguous      = errors.New("card identity is present on multiple Agents")
	ErrModemOffline       = errors.New("modem or SIM is not present on an Agent")
	ErrModemAmbiguous     = errors.New("modem and SIM identity is present on multiple Agents")
)

type Server struct {
	tokens    TokenResolver
	mu        sync.RWMutex
	agents    map[string]*serverConnection
	nextID    atomic.Uint64
	pingEvery time.Duration
	pingWait  time.Duration
}

type serverConnection struct {
	hello       Hello
	socket      *websocket.Conn
	closed      chan struct{}
	mu          sync.Mutex
	writeMu     sync.Mutex
	pending     map[string]chan envelope
	connectedAt time.Time
	lastSeen    atomic.Int64
	healthMu    sync.RWMutex
	healthSeq   uint64
	lastReport  time.Time
	topologyRev string
	topology    *TopologySnapshot
}

type ConnectionStatus struct {
	AgentID           string            `json:"agent_id"`
	ProcessGeneration string            `json:"process_generation"`
	ConnectedAt       time.Time         `json:"connected_at"`
	LastSeen          time.Time         `json:"last_seen"`
	LastReport        time.Time         `json:"last_report,omitempty"`
	TopologyRevision  string            `json:"topology_revision,omitempty"`
	Topology          *TopologySnapshot `json:"topology,omitempty"`
}

type ModemTarget struct {
	AgentID           string
	ProcessGeneration string
	AttachmentID      string
	EquipmentID       string
	CardID            string
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
	connection.readLoop(connectionContext)
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
	selected, err := server.ResolveModemTarget(command.EquipmentID, command.CardID)
	if err != nil {
		return ModemResponse{}, err
	}
	return server.ExecuteModem(ctx, selected.AgentID, selected.ProcessGeneration,
		command.requestFor(selected.AttachmentID))
}

func (server *Server) ResolveModemTarget(equipmentID, cardID string) (ModemTarget, error) {
	if !validEquipmentID(equipmentID) || !validCardID(cardID) {
		return ModemTarget{}, errors.New("invalid modem target identity")
	}
	var matches []ModemTarget
	for _, status := range server.Statuses() {
		if status.Topology == nil || status.Topology.ModemCondition != ModemReady {
			continue
		}
		for _, modem := range status.Topology.Modems {
			if modem.EquipmentID == equipmentID && modem.SIM.ICCID == cardID &&
				modem.AT.State == "ready" && modem.AT.CallSignalling {
				matches = append(matches, ModemTarget{
					AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
					AttachmentID: modem.AttachmentID, EquipmentID: equipmentID, CardID: cardID,
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
	type target struct {
		agentID, processGeneration, sessionGeneration string
	}
	var matches []target
	for _, status := range server.Statuses() {
		if status.Topology == nil || status.Topology.ReaderCondition != ReaderReady {
			continue
		}
		for _, reader := range status.Topology.Readers {
			if reader.IdentityState == CardIdentified && reader.CardID == challenge.CardID {
				matches = append(matches, target{
					agentID: status.AgentID, processGeneration: status.ProcessGeneration,
					sessionGeneration: reader.SessionGeneration,
				})
			}
		}
	}
	if len(matches) == 0 {
		return AKAResponse{}, ErrCardOffline
	}
	if len(matches) != 1 {
		return AKAResponse{}, ErrCardAmbiguous
	}
	selected := matches[0]
	return server.AuthenticateAKA(ctx, selected.agentID, selected.processGeneration,
		challenge.requestFor(selected.sessionGeneration))
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

func (connection *serverConnection) readLoop(ctx context.Context) {
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
		if message.Kind != kindAKAResponse && message.Kind != kindModemResponse && message.Kind != kindMediaResponse {
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
		if connection.healthSeq == 0 || report.TopologyRevision != connection.topologyRev {
			return errors.New("Agent health heartbeat has no matching topology")
		}
	} else {
		connection.topology = cloneTopology(report.Topology)
		connection.topologyRev = report.TopologyRevision
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
