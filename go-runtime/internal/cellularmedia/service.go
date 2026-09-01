// Package cellularmedia binds an authenticated browser media session to one
// exact Agent modem target. It owns only browser/media/call lease lifetime;
// hardware discovery, AT execution, and durable paid-call safety remain in
// Agent.
package cellularmedia

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const (
	maximumRequestBytes    = 4096
	defaultCapacity        = 64
	prepareTTL             = 2 * time.Minute
	heartbeatTimeout       = 10 * time.Second
	renewEvery             = 3 * time.Second
	incomingEventFreshness = agentlink.ModemEventRetryEvery + 3*time.Second
)

type BrowserAuth interface {
	VerifyBrowserSession(context.Context, *http.Request) (string, error)
	AuthorizeBrowserMutation(*http.Request) (string, error)
}

type Catalog interface {
	Get(string) (linecatalog.Line, error)
}

type AgentRuntime interface {
	Status(string) (agentlink.ConnectionStatus, bool)
	ResolveModemTargetForCardAction(string, agentlink.ModemAction) (agentlink.ModemTarget, error)
	ExecuteModem(context.Context, string, string, agentlink.ModemRequest) (agentlink.ModemResponse, error)
	ExecuteModemMedia(context.Context, string, string, agentlink.ModemMediaRequest) (agentlink.ModemMediaResponse, error)
}

type Config struct {
	Context  context.Context
	Auth     BrowserAuth
	Catalog  Catalog
	Agents   AgentRuntime
	Broker   *agentmedia.Broker
	Calls    CallRecorder
	Incoming IncomingCallStore
	Now      func() time.Time
	Capacity int
}

type CallRecorder interface {
	Start(lineID, transport, callID, direction, peer string, at time.Time) error
	Active(lineID, transport, callID string, at time.Time) error
	Finish(lineID, transport, callID, status string, at time.Time) error
}

type IncomingCallStore interface {
	CellularCall(string) (callhistory.CellularCallSource, bool, error)
	CurrentCellularCalls() ([]callhistory.CellularCallSource, error)
	MarkCellularAnswered(string, time.Time) error
	MarkCellularRejected(string, time.Time) error
	MarkCellularEnded(string, time.Time) error
}

type Service struct {
	config Config
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*session
	claims   map[string]*incomingClaim
	wait     sync.WaitGroup
}

type session struct {
	mu sync.Mutex

	id        string
	subject   string
	lineID    string
	callID    string
	direction string
	incoming  *incomingFence
	target    agentlink.ModemTarget
	peer      *agentmedia.Peer
	createdAt time.Time
	expiresAt time.Time

	phase           string
	lastHeartbeat   time.Time
	nextRenew       time.Time
	renewing        bool
	hangupStarted   bool
	terminal        bool
	lastFailure     string
	connection      *browserConnection
	connectionID    uint64
	connectionEpoch uint64
	challenge       string
	resumeTicket    string

	inputFrames  uint64
	inputSignal  uint64
	outputFrames uint64
	outputSignal uint64
	capture      uint64
	playback     uint64
	played       uint64
	canaryReady  bool
}

type incomingFence struct {
	eventID         string
	operationID     string
	occurrence      uint64
	nativeCallIndex int
	session         string
	number          string
}

type incomingClaim struct {
	eventID           string
	operationID       string
	action            string
	subject           string
	state             string
	resultCode        string
	sessionID         string
	lineID            string
	cardID            string
	sessionGeneration string
	nativeCallIndex   int
	occurrence        uint64
	createdAt         time.Time
	expiresAt         time.Time
}

type IncomingCallView struct {
	callhistory.CellularCallSource
	Actionable  bool   `json:"actionable"`
	Blocked     string `json:"blocked,omitempty"`
	Claiming    bool   `json:"claiming"`
	ClaimAction string `json:"claim_action,omitempty"`
}

func New(config Config) (*Service, error) {
	if config.Context == nil || config.Auth == nil || config.Catalog == nil || config.Agents == nil || config.Broker == nil {
		return nil, errors.New("invalid cellular media configuration")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Capacity == 0 {
		config.Capacity = defaultCapacity
	}
	if config.Capacity < 1 || config.Capacity > 4096 {
		return nil, errors.New("cellular media capacity must be between 1 and 4096")
	}
	ctx, cancel := context.WithCancel(config.Context)
	service := &Service{config: config, ctx: ctx, cancel: cancel, sessions: map[string]*session{}, claims: map[string]*incomingClaim{}}
	service.wait.Add(1)
	go service.watchdog()
	return service, nil
}

func (service *Service) Close() error {
	service.cancel()
	service.wait.Wait()
	service.mu.Lock()
	sessions := make([]*session, 0, len(service.sessions))
	for _, current := range service.sessions {
		sessions = append(sessions, current)
	}
	service.sessions = map[string]*session{}
	service.mu.Unlock()
	for _, current := range sessions {
		service.stopMedia(current)
	}
	return nil
}

func (service *Service) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	switch {
	case request.URL.Path == "/v1/cellular/media/leases":
		service.serveLeases(response, request)
	case strings.HasPrefix(request.URL.Path, "/api/cellular-browser-media/"):
		service.serveBrowser(response, request)
	case strings.HasPrefix(request.URL.Path, "/v1/lines/") && strings.Contains(request.URL.Path, "/cellular/calls/"):
		service.serveCall(response, request)
	default:
		http.NotFound(response, request)
	}
}

func (service *Service) serveLeases(response http.ResponseWriter, request *http.Request) {
	subject, err := service.config.Auth.AuthorizeBrowserMutation(request)
	if err != nil {
		writeJSON(response, http.StatusForbidden, map[string]string{"code": "browser_authorization_failed"})
		return
	}
	switch request.Method {
	case http.MethodPost:
		var input struct {
			LineID               string `json:"line_id"`
			CallID               string `json:"call_id"`
			ExpectedCardID       string `json:"expected_card_id"`
			OperationID          string `json:"operation_id,omitempty"`
			IncomingEventID      string `json:"incoming_event_id,omitempty"`
			SIMSessionGeneration string `json:"sim_session_generation,omitempty"`
			NativeCallIndex      int    `json:"native_call_index,omitempty"`
			CallOccurrence       uint64 `json:"call_occurrence,omitempty"`
		}
		if request.URL.RawQuery != "" || decodeRequest(request.Body, &input) != nil ||
			!validID(input.LineID) || !validID(input.CallID) || !validCardID(input.ExpectedCardID) {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_media_lease"})
			return
		}
		direction := "out"
		var incoming *incomingFence
		if input.IncomingEventID != "" {
			if input.CallID != input.IncomingEventID || !validID(input.OperationID) ||
				!validID(input.IncomingEventID) || !validID(input.SIMSessionGeneration) ||
				input.NativeCallIndex < 1 || input.CallOccurrence == 0 || service.config.Incoming == nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_incoming_claim"})
				return
			}
			source, _, existing, claimErr := service.claimIncoming(strings.TrimSpace(subject), strings.TrimSpace(input.LineID),
				strings.TrimSpace(input.OperationID), "answer", strings.TrimSpace(input.IncomingEventID),
				strings.TrimSpace(input.ExpectedCardID), strings.TrimSpace(input.SIMSessionGeneration),
				input.NativeCallIndex, input.CallOccurrence)
			if claimErr != nil {
				writeServiceError(response, claimErr)
				return
			}
			if existing != nil {
				writeJSON(response, http.StatusCreated, map[string]any{
					"session_id": existing.id, "ws_path": "/api/cellular-browser-media/" + existing.id + "/ws",
					"expires_at": existing.expiresAt,
				})
				return
			}
			direction = "in"
			incoming = &incomingFence{eventID: source.IncomingEventID, operationID: strings.TrimSpace(input.OperationID),
				occurrence: source.Occurrence, nativeCallIndex: source.NativeCallIndex,
				session: source.SIMSessionGeneration, number: source.Number}
		}
		lease, err := service.prepare(request.Context(), strings.TrimSpace(subject), strings.TrimSpace(input.LineID),
			strings.TrimSpace(input.CallID), strings.TrimSpace(input.ExpectedCardID), direction, incoming)
		if err != nil {
			if incoming != nil {
				service.releaseClaim(incoming.eventID, incoming.operationID)
			}
			writeServiceError(response, err)
			return
		}
		if incoming != nil {
			service.bindClaimSession(incoming.eventID, incoming.operationID, lease.id)
		}
		writeJSON(response, http.StatusCreated, map[string]any{
			"session_id": lease.id, "ws_path": "/api/cellular-browser-media/" + lease.id + "/ws",
			"expires_at": lease.expiresAt,
		})
	case http.MethodDelete:
		var input struct {
			SessionID string `json:"session_id"`
		}
		if request.URL.RawQuery != "" || decodeRequest(request.Body, &input) != nil || !validID(input.SessionID) {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_media_lease"})
			return
		}
		current := service.lookup(strings.TrimSpace(input.SessionID))
		if current == nil || current.subject != subject {
			writeJSON(response, http.StatusNotFound, map[string]string{"code": "cellular_media_not_found"})
			return
		}
		current.mu.Lock()
		active := current.phase == "active" || current.phase == "starting" || current.phase == "uncertain" || current.phase == "ending"
		current.mu.Unlock()
		if active {
			writeJSON(response, http.StatusConflict, map[string]string{"code": "cellular_call_must_be_hung_up"})
			return
		}
		service.remove(current)
		service.stopMedia(current)
		if current.incoming != nil {
			service.releaseClaim(current.incoming.eventID, current.incoming.operationID)
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
	}
}

func (service *Service) prepare(ctx context.Context, subject, lineID, callID, expectedCardID, direction string,
	incoming *incomingFence,
) (*session, error) {
	line, err := service.config.Catalog.Get(lineID)
	cardID, targetReady := cellularTargetIdentity(line)
	if err != nil || !targetReady {
		return nil, errCellularTargetUnavailable
	}
	if cardID != expectedCardID {
		return nil, errPaidActionCardMismatch
	}
	target, err := service.config.Agents.ResolveModemTargetForCardAction(cardID, agentlink.ModemCallStatus)
	if err != nil {
		return nil, err
	}
	sessionID, err := randomID()
	if err != nil {
		return nil, err
	}
	mediaToken, err := agentmedia.NewSessionToken()
	if err != nil {
		return nil, err
	}
	now := service.config.Now().UTC()
	current := &session{
		id: sessionID, subject: subject, lineID: lineID, callID: callID, target: target,
		direction: direction, incoming: incoming,
		createdAt: now, expiresAt: now.Add(prepareTTL), phase: "preparing", lastHeartbeat: now,
	}
	service.mu.Lock()
	if len(service.sessions) >= service.config.Capacity {
		service.mu.Unlock()
		return nil, errCapacity
	}
	service.sessions[sessionID] = current
	service.mu.Unlock()
	failed := true
	defer func() {
		if failed {
			service.remove(current)
			service.config.Broker.Revoke(sessionID)
		}
	}()
	if err := service.config.Broker.Reserve(agentmedia.Reservation{
		AgentID: target.AgentID, ProcessGeneration: target.ProcessGeneration,
		SessionID: sessionID, MediaToken: mediaToken, ExpiresAt: now.Add(30 * time.Second),
	}); err != nil {
		return nil, err
	}
	operationContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, err = service.config.Agents.ExecuteModemMedia(operationContext, target.AgentID, target.ProcessGeneration,
		agentlink.ModemMediaRequest{
			OperationID: "media-prepare-" + sessionID, AttachmentID: target.AttachmentID,
			EquipmentID: target.EquipmentID, CardID: target.CardID,
			Action: agentlink.ModemMediaPrepare, SessionID: sessionID, MediaToken: mediaToken,
		})
	cancel()
	if err != nil {
		return nil, err
	}
	acquireContext, cancelAcquire := context.WithTimeout(ctx, 5*time.Second)
	peer, err := service.config.Broker.Acquire(acquireContext, sessionID)
	cancelAcquire()
	if err != nil {
		service.stopAgentMedia(current)
		return nil, err
	}
	current.mu.Lock()
	current.peer = peer
	current.phase = "ready"
	current.mu.Unlock()
	failed = false
	return current, nil
}

func cellularTargetIdentity(line linecatalog.Line) (string, bool) {
	cardID := strings.TrimSpace(line.CardID)
	return cardID, cardID != ""
}

func (service *Service) claimIncoming(subject, lineID, operationID, action, eventID, cardID, sessionGeneration string,
	nativeIndex int, occurrence uint64,
) (callhistory.CellularCallSource, *incomingClaim, *session, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if prior := service.claims[eventID]; prior != nil {
		if prior.operationID != operationID || prior.action != action || prior.subject != subject ||
			prior.lineID != lineID || prior.cardID != cardID || prior.sessionGeneration != sessionGeneration ||
			prior.nativeCallIndex != nativeIndex || prior.occurrence != occurrence {
			return callhistory.CellularCallSource{}, nil, nil, errIncomingClaimed
		}
		source, found, err := service.config.Incoming.CellularCall(eventID)
		if err != nil || !found {
			return callhistory.CellularCallSource{}, nil, nil, errIncomingStale
		}
		if prior.sessionID == "" && prior.action == "answer" && prior.state == "claiming" {
			return callhistory.CellularCallSource{}, nil, nil, errIncomingClaimed
		}
		copy := *prior
		return source, &copy, service.sessions[prior.sessionID], nil
	}
	source, err := service.liveIncoming(lineID, eventID)
	if err != nil {
		return callhistory.CellularCallSource{}, nil, nil, err
	}
	if source.CardID != cardID || source.SIMSessionGeneration != sessionGeneration ||
		source.NativeCallIndex != nativeIndex || source.Occurrence != occurrence {
		return callhistory.CellularCallSource{}, nil, nil, errIncomingStale
	}
	now := service.config.Now().UTC()
	claim := &incomingClaim{eventID: eventID, operationID: operationID, action: action,
		subject: subject, state: "claiming", lineID: lineID, cardID: cardID,
		sessionGeneration: sessionGeneration, nativeCallIndex: nativeIndex, occurrence: occurrence,
		createdAt: now, expiresAt: now.Add(prepareTTL)}
	service.claims[eventID] = claim
	copy := *claim
	return source, &copy, nil, nil
}

func (service *Service) bindClaimSession(eventID, operationID, sessionID string) {
	service.mu.Lock()
	if claim := service.claims[eventID]; claim != nil && claim.operationID == operationID {
		claim.sessionID = sessionID
	}
	service.mu.Unlock()
}

func (service *Service) releaseClaim(eventID, operationID string) {
	service.mu.Lock()
	if claim := service.claims[eventID]; claim != nil && claim.operationID == operationID {
		delete(service.claims, eventID)
	}
	service.mu.Unlock()
}

func (service *Service) finishClaim(eventID, operationID, state, code string) {
	service.mu.Lock()
	if claim := service.claims[eventID]; claim != nil && claim.operationID == operationID {
		claim.state, claim.resultCode = state, code
		claim.expiresAt = service.config.Now().UTC().Add(prepareTTL)
	}
	service.mu.Unlock()
}

func (service *Service) liveIncoming(lineID, eventID string) (callhistory.CellularCallSource, error) {
	if service.config.Incoming == nil {
		return callhistory.CellularCallSource{}, errIncomingUnavailable
	}
	source, found, err := service.config.Incoming.CellularCall(eventID)
	if err != nil {
		return callhistory.CellularCallSource{}, err
	}
	if !found || source.LineID != lineID || source.State != "ringing_in" ||
		service.config.Now().UTC().Sub(source.ReceivedAt) > incomingEventFreshness {
		return callhistory.CellularCallSource{}, errIncomingStale
	}
	line, err := service.config.Catalog.Get(lineID)
	if err != nil || strings.TrimSpace(line.CardID) != source.CardID {
		return callhistory.CellularCallSource{}, errIncomingStale
	}
	target, err := service.config.Agents.ResolveModemTargetForCardAction(source.CardID, agentlink.ModemCallStatus)
	if err != nil || target.AgentID != source.AgentID || target.ProcessGeneration != source.ProcessGeneration ||
		target.AttachmentID != source.AttachmentID || target.EquipmentID != source.EquipmentID {
		return callhistory.CellularCallSource{}, errIncomingStale
	}
	status, found := service.config.Agents.Status(source.AgentID)
	if !found || status.ProcessGeneration != source.ProcessGeneration || !incomingTopologyFence(status.Topology, source) {
		return callhistory.CellularCallSource{}, errIncomingStale
	}
	return source, nil
}

func incomingTopologyFence(topology *agentlink.TopologySnapshot, source callhistory.CellularCallSource) bool {
	if topology == nil || topology.ModemCondition != agentlink.ModemReady {
		return false
	}
	matches := 0
	for _, modem := range topology.Modems {
		if modem.AttachmentID == source.AttachmentID && modem.EquipmentID == source.EquipmentID &&
			modem.SIM.ICCID == source.CardID && modem.SIM.SessionGeneration == source.SIMSessionGeneration &&
			modem.Condition == "ready" && modem.SIM.State == "ready" && modem.AT.State == "ready" &&
			modem.AT.CallSignalling {
			matches++
		}
	}
	return matches == 1
}

func (service *Service) IncomingCalls() ([]IncomingCallView, error) {
	if service.config.Incoming == nil {
		return []IncomingCallView{}, nil
	}
	sources, err := service.config.Incoming.CurrentCellularCalls()
	if err != nil {
		return nil, err
	}
	result := make([]IncomingCallView, 0, len(sources))
	for _, source := range sources {
		view := IncomingCallView{CellularCallSource: source}
		if source.State == "ringing_in" {
			_, liveErr := service.liveIncoming(source.LineID, source.IncomingEventID)
			view.Actionable = liveErr == nil
			if liveErr != nil {
				view.Blocked = "incoming_state_stale"
			}
		} else {
			view.Blocked = "line_occupied"
		}
		service.mu.Lock()
		if claim := service.claims[source.IncomingEventID]; claim != nil {
			view.Actionable, view.Claiming, view.ClaimAction = false, true, claim.action
			view.Blocked = "incoming_claimed"
		}
		service.mu.Unlock()
		result = append(result, view)
	}
	return result, nil
}

func (service *Service) lookup(sessionID string) *session {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.sessions[sessionID]
}

func (service *Service) remove(expected *session) {
	service.mu.Lock()
	if service.sessions[expected.id] == expected {
		delete(service.sessions, expected.id)
	}
	service.mu.Unlock()
}

func (service *Service) stopMedia(current *session) {
	current.mu.Lock()
	connection := current.connection
	current.connection = nil
	current.mu.Unlock()
	if connection != nil {
		connection.close(websocket.StatusNormalClosure, "cellular media ended")
	}
	service.config.Broker.Revoke(current.id)
	service.stopAgentMedia(current)
}

func (service *Service) stopAgentMedia(current *session) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, _ = service.config.Agents.ExecuteModemMedia(ctx, current.target.AgentID, current.target.ProcessGeneration,
		agentlink.ModemMediaRequest{
			OperationID: "media-stop-" + current.id, AttachmentID: current.target.AttachmentID,
			EquipmentID: current.target.EquipmentID, CardID: current.target.CardID,
			Action: agentlink.ModemMediaStop, SessionID: current.id,
		})
	cancel()
}

func (service *Service) watchdog() {
	defer service.wait.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-service.ctx.Done():
			return
		case <-ticker.C:
			service.sweep(service.config.Now().UTC())
		}
	}
}

func (service *Service) sweep(now time.Time) {
	service.mu.Lock()
	sessions := make([]*session, 0, len(service.sessions))
	for _, current := range service.sessions {
		sessions = append(sessions, current)
	}
	service.mu.Unlock()
	for _, current := range sessions {
		current.mu.Lock()
		phase := current.phase
		if phase == "ready" && !current.expiresAt.After(now) {
			current.phase = "expired"
			incoming := current.incoming
			current.mu.Unlock()
			service.remove(current)
			service.stopMedia(current)
			if incoming != nil {
				service.releaseClaim(incoming.eventID, incoming.operationID)
			}
			continue
		}
		active := phase == "active" || phase == "uncertain"
		if active && now.Sub(current.lastHeartbeat) >= heartbeatTimeout && !current.hangupStarted {
			current.hangupStarted = true
			current.phase = "ending"
			current.mu.Unlock()
			go service.terminate(current, "browser_heartbeat_timeout")
			continue
		}
		if active && !current.renewing && !now.Before(current.nextRenew) && now.Sub(current.lastHeartbeat) < heartbeatTimeout {
			current.renewing = true
			current.mu.Unlock()
			go service.renew(current)
			continue
		}
		current.mu.Unlock()
	}
	service.mu.Lock()
	for eventID, claim := range service.claims {
		if claim.expiresAt.After(now) {
			continue
		}
		if claim.state != "uncertain" && claim.state != "active" {
			delete(service.claims, eventID)
			continue
		}
		source, found, err := service.config.Incoming.CellularCall(eventID)
		if err == nil && (!found || source.State == "idle") {
			delete(service.claims, eventID)
		}
	}
	service.mu.Unlock()
}

var (
	errCapacity                  = errors.New("cellular media capacity is exhausted")
	errCellularTargetUnavailable = errors.New("cellular modem target is unavailable")
	errPaidActionCardMismatch    = errors.New("selected SIM identity changed before the carrier action")
	errIncomingUnavailable       = errors.New("cellular incoming call support is unavailable")
	errIncomingStale             = errors.New("cellular incoming call state is stale")
	errIncomingClaimed           = errors.New("cellular incoming call is claimed by another browser operation")
)

func randomID() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeRequest(body io.Reader, target any) error {
	payload, err := io.ReadAll(io.LimitReader(body, maximumRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumRequestBytes {
		return errors.New("invalid request size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request has trailing JSON")
	}
	return nil
}

func validID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			continue
		}
		return false
	}
	return true
}

func validCardID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeServiceError(response http.ResponseWriter, err error) {
	status, code := http.StatusBadGateway, "cellular_media_failed"
	var remote *agentlink.RemoteError
	switch {
	case errors.Is(err, errCapacity):
		status, code = http.StatusServiceUnavailable, "cellular_media_capacity"
	case errors.Is(err, errCellularTargetUnavailable), errors.Is(err, agentlink.ErrModemOffline):
		status, code = http.StatusPreconditionFailed, "cellular_modem_unavailable"
	case errors.Is(err, errPaidActionCardMismatch):
		status, code = http.StatusConflict, "paid_action_card_mismatch"
	case errors.Is(err, errIncomingStale), errors.Is(err, errIncomingUnavailable):
		status, code = http.StatusConflict, "cellular_incoming_stale"
	case errors.Is(err, errIncomingClaimed):
		status, code = http.StatusConflict, "cellular_incoming_claimed"
	case errors.Is(err, agentlink.ErrModemAmbiguous), errors.Is(err, agentlink.ErrGenerationMismatch):
		status, code = http.StatusConflict, "cellular_modem_identity_conflict"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusGatewayTimeout, "cellular_media_timeout"
	case errors.As(err, &remote) && remote.Code == "modem_incoming_call_changed":
		status, code = http.StatusConflict, remote.Code
	case errors.As(err, &remote) && remote.Code == "modem_incoming_reject_uncertain":
		status, code = http.StatusBadGateway, remote.Code
	}
	writeJSON(response, status, map[string]string{"code": code, "detail": fmt.Sprintf("%v", err)})
}
