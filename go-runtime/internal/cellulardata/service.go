// Package cellulardata exposes one explicit, revocable, quota-limited SOCKS5
// session backed by an exact Agent modem. Sessions are memory-only: Core or
// Agent restart always returns to the persistent fail-closed guard.
package cellulardata

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/bufferpool"
)

const (
	defaultTTL      = 15 * time.Minute
	maximumTTL      = 24 * time.Hour
	defaultMaxBytes = 100 << 20
	maximumMaxBytes = 1 << 40
)

type Catalog interface {
	Get(string) (linecatalog.Line, error)
}

type AgentRuntime interface {
	ResolveModemDataTarget(string, string) (agentlink.ModemTarget, error)
	ExecuteModemData(context.Context, string, string, agentlink.ModemDataRequest) (agentlink.ModemDataResponse, error)
}

type Config struct {
	Context context.Context
	Catalog Catalog
	Agents  AgentRuntime
	Broker  *agentdata.Broker
	Now     func() time.Time
	Listen  string
}

type Service struct {
	config Config
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	byLine map[string]*session
	byID   map[string]*session
	wait   sync.WaitGroup
}

type session struct {
	service   *Service
	id        string
	lineID    string
	target    agentlink.ModemTarget
	profile   string
	username  string
	password  string
	expiresAt time.Time
	maxBytes  uint64
	used      atomic.Uint64
	listener  net.Listener
	port      int
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	streams   map[string]net.Conn
	stopOnce  sync.Once
}

type sessionView struct {
	SessionID  string    `json:"session_id"`
	LineID     string    `json:"line_id"`
	State      string    `json:"state"`
	Profile    string    `json:"profile"`
	ListenPort int       `json:"listen_port"`
	Username   string    `json:"username,omitempty"`
	Password   string    `json:"password,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
	MaxBytes   uint64    `json:"max_bytes"`
	UsedBytes  uint64    `json:"used_bytes"`
}

func New(config Config) (*Service, error) {
	if config.Context == nil || config.Catalog == nil || config.Agents == nil || config.Broker == nil {
		return nil, errors.New("invalid cellular data configuration")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Listen == "" {
		config.Listen = "0.0.0.0:0"
	}
	host, port, err := net.SplitHostPort(config.Listen)
	if err != nil || port != "0" || net.ParseIP(host) == nil {
		return nil, errors.New("cellular data listener must use a literal IP and ephemeral port")
	}
	ctx, cancel := context.WithCancel(config.Context)
	return &Service{config: config, ctx: ctx, cancel: cancel, byLine: map[string]*session{}, byID: map[string]*session{}}, nil
}

func (service *Service) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "v1" || parts[1] != "lines" || parts[3] != "cellular" || parts[4] != "data" {
		http.NotFound(response, request)
		return
	}
	lineID := strings.TrimSpace(parts[2])
	if len(parts) == 6 && parts[5] == "sessions" {
		switch request.Method {
		case http.MethodPost:
			service.createHTTP(response, request, lineID)
		case http.MethodGet:
			service.statusHTTP(response, lineID)
		default:
			writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		}
		return
	}
	if len(parts) == 7 && parts[5] == "sessions" && request.Method == http.MethodDelete {
		service.stopHTTP(response, lineID, parts[6])
		return
	}
	http.NotFound(response, request)
}

func (service *Service) createHTTP(response http.ResponseWriter, request *http.Request, lineID string) {
	var input struct {
		TTLSeconds int64  `json:"ttl_seconds"`
		MaxBytes   uint64 `json:"max_bytes"`
		Profile    string `json:"profile,omitempty"`
	}
	if request.URL.RawQuery != "" || decodeRequest(request.Body, &input) != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_data_session"})
		return
	}
	if input.TTLSeconds == 0 {
		input.TTLSeconds = int64(defaultTTL.Seconds())
	}
	if input.MaxBytes == 0 {
		input.MaxBytes = defaultMaxBytes
	}
	if input.TTLSeconds < 60 || input.TTLSeconds > int64(maximumTTL.Seconds()) || input.MaxBytes < 1024 || input.MaxBytes > maximumMaxBytes || len(input.Profile) > 256 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_data_limits"})
		return
	}
	current, err := service.create(request.Context(), lineID, strings.TrimSpace(input.Profile), time.Duration(input.TTLSeconds)*time.Second, input.MaxBytes)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, current.view(true))
}

func (service *Service) statusHTTP(response http.ResponseWriter, lineID string) {
	service.mu.Lock()
	current := service.byLine[lineID]
	service.mu.Unlock()
	if current == nil {
		writeJSON(response, http.StatusOK, map[string]any{"state": "stopped", "line_id": lineID})
		return
	}
	writeJSON(response, http.StatusOK, current.view(false))
}

func (service *Service) stopHTTP(response http.ResponseWriter, lineID, sessionID string) {
	service.mu.Lock()
	current := service.byID[sessionID]
	service.mu.Unlock()
	if current == nil || current.lineID != lineID {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "cellular_data_session_not_found"})
		return
	}
	current.stop("explicit")
	response.WriteHeader(http.StatusNoContent)
}

func (service *Service) create(ctx context.Context, lineID, profile string, ttl time.Duration, maxBytes uint64) (*session, error) {
	line, err := service.config.Catalog.Get(lineID)
	if err != nil {
		return nil, err
	}
	equipmentID, cardID := strings.TrimSpace(line.SIM.IMEI), strings.TrimSpace(line.CardID)
	if equipmentID == "" || cardID == "" {
		return nil, errors.New("line has no exact cellular modem target")
	}
	target, err := service.config.Agents.ResolveModemDataTarget(equipmentID, cardID)
	if err != nil {
		return nil, err
	}
	service.mu.Lock()
	if service.byLine[lineID] != nil {
		service.mu.Unlock()
		return nil, errors.New("line already has an active cellular data session")
	}
	service.mu.Unlock()
	sessionID, err := randomID("data")
	if err != nil {
		return nil, err
	}
	username, err := randomID("mdd")
	if err != nil {
		return nil, err
	}
	password, err := randomToken()
	if err != nil {
		return nil, err
	}
	now := service.config.Now().UTC()
	expiresAt := now.Add(ttl)
	if expiresAt.Before(now) {
		return nil, errors.New("invalid cellular data expiry")
	}
	operation, err := randomID("prepare")
	if err != nil {
		return nil, err
	}
	operationContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	prepared, err := service.config.Agents.ExecuteModemData(operationContext, target.AgentID, target.ProcessGeneration, agentlink.ModemDataRequest{
		OperationID: operation, AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID, CardID: target.CardID,
		Action: agentlink.ModemDataPrepare, SessionID: sessionID, Profile: profile, ExpiresAt: expiresAt, MaxBytes: maxBytes,
	})
	cancel()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", service.config.Listen)
	if err != nil {
		service.stopAgent(target, sessionID)
		return nil, err
	}
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := net.LookupPort("tcp", portText)
	sessionContext, sessionCancel := context.WithDeadline(service.ctx, expiresAt)
	current := &session{service: service, id: sessionID, lineID: lineID, target: target, profile: prepared.Profile,
		username: username, password: password, expiresAt: expiresAt, maxBytes: maxBytes,
		listener: listener, port: port, ctx: sessionContext, cancel: sessionCancel, streams: map[string]net.Conn{}}
	service.mu.Lock()
	if service.byLine[lineID] != nil || service.ctx.Err() != nil {
		service.mu.Unlock()
		_ = listener.Close()
		sessionCancel()
		service.stopAgent(target, sessionID)
		return nil, errors.New("cellular data ownership changed")
	}
	service.byLine[lineID], service.byID[sessionID] = current, current
	service.wait.Add(2)
	service.mu.Unlock()
	go current.serve()
	go func() { defer service.wait.Done(); <-sessionContext.Done(); current.stop("expired") }()
	return current, nil
}

func (current *session) serve() {
	defer current.service.wait.Done()
	server := socks5.NewServer(socks5.WithCredential(socks5.StaticCredentials{current.username: current.password}),
		socks5.WithRule(socks5.NewPermitConnAndAss()), socks5.WithBufferPool(bufferpool.NewPool(65535)),
		socks5.WithResolver(remoteResolver{}), socks5.WithDial(current.dial))
	_ = server.Serve(current.listener)
	if current.ctx.Err() == nil {
		current.stop("listener_failed")
	}
}

// remoteResolver preserves SOCKS domain names so resolution happens on the
// Agent side instead of accidentally using Core's network and location.
type remoteResolver struct{}

func (remoteResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

func (current *session) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if current.ctx.Err() != nil || current.used.Load() >= current.maxBytes {
		return nil, errors.New("cellular data session is closed or exhausted")
	}
	streamID, err := randomID("stream")
	if err != nil {
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	if err := current.service.config.Broker.Reserve(agentdata.Reservation{AgentID: current.target.AgentID,
		ProcessGeneration: current.target.ProcessGeneration, SessionID: current.id, StreamID: streamID,
		StreamToken: token, Network: network, ExpiresAt: current.expiresAt}); err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			current.service.config.Broker.Revoke(streamID)
		}
	}()
	operation, err := randomID("open")
	if err != nil {
		return nil, err
	}
	openContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, err = current.service.config.Agents.ExecuteModemData(openContext, current.target.AgentID, current.target.ProcessGeneration, agentlink.ModemDataRequest{
		OperationID: operation, AttachmentID: current.target.AttachmentID, EquipmentID: current.target.EquipmentID,
		CardID: current.target.CardID, Action: agentlink.ModemDataOpen, SessionID: current.id,
		StreamID: streamID, StreamToken: token, Network: network, Address: address,
		ExpiresAt: current.expiresAt, MaxBytes: current.maxBytes,
	})
	cancel()
	if err != nil {
		return nil, err
	}
	acquireContext, acquireCancel := context.WithTimeout(ctx, 5*time.Second)
	conn, err := current.service.config.Broker.Acquire(acquireContext, streamID)
	acquireCancel()
	if err != nil {
		return nil, err
	}
	tracked := &quotaConn{Conn: conn, session: current, streamID: streamID}
	current.mu.Lock()
	if current.ctx.Err() != nil {
		current.mu.Unlock()
		_ = tracked.Close()
		return nil, context.Canceled
	}
	current.streams[streamID] = tracked
	current.mu.Unlock()
	failed = false
	return tracked, nil
}

type quotaConn struct {
	net.Conn
	session  *session
	streamID string
	once     sync.Once
}

func (conn *quotaConn) Read(buffer []byte) (int, error) {
	n, err := conn.Conn.Read(buffer)
	if n > 0 && conn.consume(uint64(n)) != nil {
		return n, errors.New("cellular data quota exceeded")
	}
	return n, err
}
func (conn *quotaConn) Write(payload []byte) (int, error) {
	if conn.consume(uint64(len(payload))) != nil {
		return 0, errors.New("cellular data quota exceeded")
	}
	return conn.Conn.Write(payload)
}
func (conn *quotaConn) consume(size uint64) error {
	if conn.session.used.Add(size) > conn.session.maxBytes {
		go conn.session.stop("quota")
		return errors.New("quota exceeded")
	}
	return nil
}
func (conn *quotaConn) Close() error {
	var err error
	conn.once.Do(func() {
		err = conn.Conn.Close()
		conn.session.service.config.Broker.Revoke(conn.streamID)
		conn.session.mu.Lock()
		delete(conn.session.streams, conn.streamID)
		conn.session.mu.Unlock()
	})
	return err
}

func (current *session) stop(_ string) {
	current.stopOnce.Do(func() {
		current.cancel()
		_ = current.listener.Close()
		current.service.mu.Lock()
		if current.service.byLine[current.lineID] == current {
			delete(current.service.byLine, current.lineID)
		}
		delete(current.service.byID, current.id)
		current.service.mu.Unlock()
		current.mu.Lock()
		streams := make([]net.Conn, 0, len(current.streams))
		for _, stream := range current.streams {
			streams = append(streams, stream)
		}
		current.streams = map[string]net.Conn{}
		current.mu.Unlock()
		current.service.config.Broker.RevokeSession(current.id)
		for _, stream := range streams {
			_ = stream.Close()
		}
		current.service.stopAgent(current.target, current.id)
	})
}

func (service *Service) stopAgent(target agentlink.ModemTarget, sessionID string) {
	operation, err := randomID("stop")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	_, _ = service.config.Agents.ExecuteModemData(ctx, target.AgentID, target.ProcessGeneration, agentlink.ModemDataRequest{
		OperationID: operation, AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID,
		CardID: target.CardID, Action: agentlink.ModemDataStop, SessionID: sessionID,
	})
	cancel()
}

func (current *session) view(credentials bool) sessionView {
	result := sessionView{SessionID: current.id, LineID: current.lineID, State: "ready", Profile: current.profile, ListenPort: current.port,
		ExpiresAt: current.expiresAt, MaxBytes: current.maxBytes, UsedBytes: current.used.Load()}
	if credentials {
		result.Username, result.Password = current.username, current.password
	}
	return result
}

func (service *Service) Close() error {
	service.cancel()
	service.mu.Lock()
	sessions := make([]*session, 0, len(service.byID))
	for _, current := range service.byID {
		sessions = append(sessions, current)
	}
	service.mu.Unlock()
	for _, current := range sessions {
		current.stop("core_shutdown")
	}
	service.wait.Wait()
	return nil
}

func randomID(prefix string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	return prefix + "-" + token[:22], nil
}
func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeRequest(reader io.Reader, target any) error {
	payload, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil || len(payload) > 4096 {
		return errors.New("request too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing request data")
	}
	return nil
}
func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
func writeServiceError(response http.ResponseWriter, err error) {
	status := http.StatusConflict
	code := "cellular_data_unavailable"
	if errors.Is(err, linecatalog.ErrNotFound) {
		status = http.StatusNotFound
		code = "line_not_found"
	}
	writeJSON(response, status, map[string]string{"code": code, "detail": fmt.Sprint(err)})
}
