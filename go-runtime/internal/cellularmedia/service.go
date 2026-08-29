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
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const (
	maximumRequestBytes = 4096
	defaultCapacity     = 64
	prepareTTL          = 2 * time.Minute
	heartbeatTimeout    = 10 * time.Second
	renewEvery          = 3 * time.Second
)

type BrowserAuth interface {
	VerifyBrowserSession(context.Context, *http.Request) (string, error)
	AuthorizeBrowserMutation(*http.Request) (string, error)
}

type Catalog interface {
	Get(string) (linecatalog.Line, error)
}

type AgentRuntime interface {
	ResolveModemTarget(string, string) (agentlink.ModemTarget, error)
	ExecuteModem(context.Context, string, string, agentlink.ModemRequest) (agentlink.ModemResponse, error)
	ExecuteModemMedia(context.Context, string, string, agentlink.ModemMediaRequest) (agentlink.ModemMediaResponse, error)
}

type Config struct {
	Context  context.Context
	Auth     BrowserAuth
	Catalog  Catalog
	Agents   AgentRuntime
	Broker   *agentmedia.Broker
	Now      func() time.Time
	Capacity int
}

type Service struct {
	config Config
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*session
	wait     sync.WaitGroup
}

type session struct {
	mu sync.Mutex

	id        string
	subject   string
	lineID    string
	callID    string
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
	service := &Service{config: config, ctx: ctx, cancel: cancel, sessions: map[string]*session{}}
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
			LineID string `json:"line_id"`
			CallID string `json:"call_id"`
		}
		if request.URL.RawQuery != "" || decodeRequest(request.Body, &input) != nil ||
			!validID(input.LineID) || !validID(input.CallID) {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_media_lease"})
			return
		}
		lease, err := service.prepare(request.Context(), strings.TrimSpace(subject), strings.TrimSpace(input.LineID), strings.TrimSpace(input.CallID))
		if err != nil {
			writeServiceError(response, err)
			return
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
		response.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
	}
}

func (service *Service) prepare(ctx context.Context, subject, lineID, callID string) (*session, error) {
	line, err := service.config.Catalog.Get(lineID)
	equipmentID, cardID, targetReady := cellularTargetIdentity(line)
	if err != nil || !targetReady {
		return nil, errCellularTargetUnavailable
	}
	target, err := service.config.Agents.ResolveModemTarget(equipmentID, cardID)
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

func cellularTargetIdentity(line linecatalog.Line) (string, string, bool) {
	equipmentID := strings.TrimSpace(line.SIM.IMEI)
	cardID := strings.TrimSpace(line.CardID)
	return equipmentID, cardID, equipmentID != "" && cardID != ""
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
			current.mu.Unlock()
			service.remove(current)
			service.stopMedia(current)
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
}

var (
	errCapacity                  = errors.New("cellular media capacity is exhausted")
	errCellularTargetUnavailable = errors.New("cellular modem target is unavailable")
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

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeServiceError(response http.ResponseWriter, err error) {
	status, code := http.StatusBadGateway, "cellular_media_failed"
	switch {
	case errors.Is(err, errCapacity):
		status, code = http.StatusServiceUnavailable, "cellular_media_capacity"
	case errors.Is(err, errCellularTargetUnavailable), errors.Is(err, agentlink.ErrModemOffline):
		status, code = http.StatusPreconditionFailed, "cellular_modem_unavailable"
	case errors.Is(err, agentlink.ErrModemAmbiguous), errors.Is(err, agentlink.ErrGenerationMismatch):
		status, code = http.StatusConflict, "cellular_modem_identity_conflict"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusGatewayTimeout, "cellular_media_timeout"
	}
	writeJSON(response, status, map[string]string{"code": code, "detail": fmt.Sprintf("%v", err)})
}
