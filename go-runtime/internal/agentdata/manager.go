package agentdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type Target struct {
	AttachmentID string
	EquipmentID  string
	CardID       string
}

type Backend interface {
	PrepareData(context.Context, Target, string) (string, error)
	DialData(context.Context, Target, string, string) (net.Conn, error)
	StopData(context.Context, Target) error
}

type Config struct {
	Context           context.Context
	ServerURL         string
	ServerToken       string
	AgentID           string
	ProcessGeneration string
	HTTPClient        *http.Client
	Backend           Backend
	Coordinator       agentmodem.AuxiliaryCoordinator
}

type Manager struct {
	config Config
	url    string
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	// operationMu is the device-lifecycle boundary shared with call/raw work.
	// It is intentionally global because the underlying platform adapters may
	// use one ModemManager/MBN owner while resolving an exact equipment ID.
	operationMu sync.Mutex
	items       map[string]*dataSession
	cleanup     map[string]*dataSession
	wait        sync.WaitGroup
	closed      bool
}

var ErrSessionActive = errors.New("cellular data session is active on this modem")

type dataSession struct {
	target    Target
	id        string
	profile   string
	expiresAt time.Time
	maxBytes  uint64
	used      atomic.Uint64
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	streams   map[string]net.Conn
	stopMu    sync.Mutex
	stopErr   error
}

func NewManager(config Config) (*Manager, error) {
	if config.Context == nil || config.HTTPClient == nil || config.Backend == nil || config.Coordinator == nil || len(config.ServerToken) < 32 ||
		strings.TrimSpace(config.AgentID) == "" || strings.TrimSpace(config.ProcessGeneration) == "" {
		return nil, errors.New("invalid Agent data configuration")
	}
	dataURL, err := agentDataURL(config.ServerURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(config.Context)
	return &Manager{config: config, url: dataURL, ctx: ctx, cancel: cancel,
		items: map[string]*dataSession{}, cleanup: map[string]*dataSession{}}, nil
}

func (manager *Manager) ExecuteModemData(ctx context.Context, request agentlink.ModemDataRequest) agentlink.ModemDataResponse {
	response := agentlink.ModemDataResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID, SessionID: request.SessionID, StreamID: request.StreamID}
	if err := request.Validate(); err != nil {
		response.Failure = &agentlink.RemoteError{Kind: "rejected", Code: "invalid_modem_data_request"}
		return response
	}
	var profile string
	var err error
	switch request.Action {
	case agentlink.ModemDataPrepare:
		profile, err = manager.prepare(ctx, request)
		if err == nil {
			response.State, response.Profile = "ready", profile
		}
	case agentlink.ModemDataOpen:
		err = manager.open(ctx, request)
		if err == nil {
			response.State = "open"
		}
	case agentlink.ModemDataStop:
		err = manager.stop(ctx, request)
		if err == nil {
			response.State = "stopped"
		}
	}
	if err != nil {
		response.Failure = dataFailure(err)
	}
	return response
}

func (manager *Manager) prepare(ctx context.Context, request agentlink.ModemDataRequest) (string, error) {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	var profile string
	err := manager.config.Coordinator.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
		var prepareErr error
		profile, prepareErr = manager.prepareLocked(operationContext, request)
		return prepareErr
	})
	return profile, err
}

func (manager *Manager) prepareLocked(ctx context.Context, request agentlink.ModemDataRequest) (string, error) {
	target := targetFor(request)
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return "", context.Canceled
	}
	if current := manager.items[request.EquipmentID]; current != nil {
		same := current.id == request.SessionID && current.target == target && current.expiresAt.Equal(request.ExpiresAt) && current.maxBytes == request.MaxBytes
		manager.mu.Unlock()
		if same {
			return current.profile, nil
		}
		return "", errors.New("another data session owns this modem")
	}
	if manager.cleanup[request.EquipmentID] != nil {
		manager.mu.Unlock()
		return "", errors.New("previous data session cleanup still owns this modem")
	}
	manager.mu.Unlock()
	profile, err := manager.config.Backend.PrepareData(ctx, target, request.Profile)
	if err != nil {
		return "", fmt.Errorf("prepare cellular data: %w", err)
	}
	sessionContext, cancel := context.WithDeadline(manager.ctx, request.ExpiresAt)
	current := &dataSession{target: target, id: request.SessionID, profile: profile, expiresAt: request.ExpiresAt,
		maxBytes: request.MaxBytes, ctx: sessionContext, cancel: cancel, streams: map[string]net.Conn{}}
	manager.mu.Lock()
	if manager.closed || manager.items[request.EquipmentID] != nil {
		manager.mu.Unlock()
		cancel()
		stopContext, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = manager.config.Backend.StopData(stopContext, target)
		stopCancel()
		return "", errors.New("data session ownership changed during preparation")
	}
	manager.items[request.EquipmentID] = current
	manager.wait.Add(1)
	manager.mu.Unlock()
	go manager.watch(request.EquipmentID, current)
	return profile, nil
}

func (manager *Manager) open(ctx context.Context, request agentlink.ModemDataRequest) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	return manager.config.Coordinator.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
		return manager.openLocked(operationContext, request)
	})
}

func (manager *Manager) openLocked(ctx context.Context, request agentlink.ModemDataRequest) error {
	manager.mu.Lock()
	current := manager.items[request.EquipmentID]
	manager.mu.Unlock()
	if current == nil || current.id != request.SessionID || current.target != targetFor(request) ||
		!current.expiresAt.Equal(request.ExpiresAt) || current.maxBytes != request.MaxBytes {
		return errors.New("data session target or lifetime was replaced")
	}
	if current.used.Load() >= current.maxBytes {
		return errors.New("data session quota is exhausted")
	}
	current.mu.Lock()
	if _, exists := current.streams[request.StreamID]; exists {
		current.mu.Unlock()
		return nil
	}
	current.mu.Unlock()
	remote, err := manager.config.Backend.DialData(ctx, current.target, request.Network, request.Address)
	if err != nil {
		return fmt.Errorf("dial cellular destination: %w", err)
	}
	socket, err := manager.dial(ctx, request)
	if err != nil {
		_ = remote.Close()
		return err
	}
	current.mu.Lock()
	if current.ctx.Err() != nil || current.streams[request.StreamID] != nil {
		current.mu.Unlock()
		socket.CloseNow()
		_ = remote.Close()
		return errors.New("data stream ownership changed")
	}
	current.streams[request.StreamID] = remote
	current.mu.Unlock()
	manager.wait.Add(1)
	go manager.bridge(current, request, remote, socket)
	return nil
}

func (manager *Manager) dial(ctx context.Context, request agentlink.ModemDataRequest) (*websocket.Conn, error) {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+manager.config.ServerToken)
	headers.Set("X-MDD-Agent-ID", manager.config.AgentID)
	headers.Set("X-MDD-Agent-Generation", manager.config.ProcessGeneration)
	headers.Set("X-MDD-Data-Session", request.SessionID)
	headers.Set("X-MDD-Data-Stream", request.StreamID)
	headers.Set("X-MDD-Data-Token", request.StreamToken)
	socket, response, err := websocket.Dial(ctx, manager.url, &websocket.DialOptions{HTTPClient: manager.config.HTTPClient,
		HTTPHeader: headers, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("connect Agent data WSS: HTTP %d: %w", response.StatusCode, err)
		}
		return nil, fmt.Errorf("connect Agent data WSS: %w", err)
	}
	socket.SetReadLimit(maximumDatagram)
	ackContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	kind, payload, err := socket.Read(ackContext)
	cancel()
	var ack struct {
		Type     string `json:"type"`
		Version  int    `json:"version"`
		StreamID string `json:"stream_id"`
	}
	if err == nil && kind == websocket.MessageText {
		err = json.Unmarshal(payload, &ack)
	}
	if err != nil || kind != websocket.MessageText || ack.Type != "agent.data.ready" || ack.Version != 1 || ack.StreamID != request.StreamID {
		_ = socket.Close(websocket.StatusPolicyViolation, "invalid data acknowledgement")
		return nil, errors.New("Core did not acknowledge exact Agent data stream")
	}
	return socket, nil
}

func (manager *Manager) bridge(current *dataSession, request agentlink.ModemDataRequest, remote net.Conn, socket *websocket.Conn) {
	defer manager.wait.Done()
	defer func() {
		socket.CloseNow()
		_ = remote.Close()
		current.mu.Lock()
		delete(current.streams, request.StreamID)
		current.mu.Unlock()
	}()
	var err error
	if request.Network == "tcp" {
		err = bridgeTCP(current, remote, socket)
	} else {
		err = bridgeUDP(current, remote, socket)
	}
	if err != nil && current.ctx.Err() == nil && current.used.Load() >= current.maxBytes {
		current.cancel()
	}
}

func bridgeTCP(current *dataSession, remote net.Conn, socket *websocket.Conn) error {
	web := websocket.NetConn(current.ctx, socket, websocket.MessageBinary)
	defer web.Close()
	results := make(chan error, 2)
	go func() { _, err := io.Copy(&quotaWriter{session: current, target: remote}, web); results <- err }()
	go func() { _, err := io.Copy(&quotaWriter{session: current, target: web}, remote); results <- err }()
	err := <-results
	_ = remote.Close()
	_ = web.Close()
	<-results
	return err
}

func bridgeUDP(current *dataSession, remote net.Conn, socket *websocket.Conn) error {
	results := make(chan error, 2)
	go func() {
		buffer := make([]byte, maximumDatagram)
		for {
			n, err := remote.Read(buffer)
			if err == nil {
				err = writeQuotaFrame(current, socket, buffer[:n])
			}
			if err != nil {
				results <- err
				return
			}
		}
	}()
	go func() {
		for {
			kind, payload, err := socket.Read(current.ctx)
			if err == nil && kind != websocket.MessageBinary {
				err = errors.New("invalid UDP data frame")
			}
			if err == nil {
				err = consume(current, uint64(len(payload)))
				if err == nil {
					_, err = remote.Write(payload)
				}
			}
			if err != nil {
				results <- err
				return
			}
		}
	}()
	err := <-results
	socket.CloseNow()
	_ = remote.Close()
	<-results
	return err
}

type quotaWriter struct {
	session *dataSession
	target  io.Writer
}

func (writer *quotaWriter) Write(payload []byte) (int, error) {
	if err := consume(writer.session, uint64(len(payload))); err != nil {
		return 0, err
	}
	return writer.target.Write(payload)
}

func writeQuotaFrame(current *dataSession, socket *websocket.Conn, payload []byte) error {
	if err := consume(current, uint64(len(payload))); err != nil {
		return err
	}
	return socket.Write(current.ctx, websocket.MessageBinary, payload)
}

func consume(current *dataSession, count uint64) error {
	used := current.used.Add(count)
	if used > current.maxBytes {
		current.cancel()
		return errors.New("data session quota exceeded")
	}
	return nil
}

func (manager *Manager) watch(equipmentID string, current *dataSession) {
	defer manager.wait.Done()
	<-current.ctx.Done()
	delay := time.Second
	for {
		manager.operationMu.Lock()
		manager.mu.Lock()
		if manager.items[equipmentID] == current {
			delete(manager.items, equipmentID)
			manager.cleanup[equipmentID] = current
		}
		owned := manager.cleanup[equipmentID] == current
		manager.mu.Unlock()
		var err error
		if owned {
			err = manager.closeSession(current)
			if err == nil {
				manager.mu.Lock()
				if manager.cleanup[equipmentID] == current {
					delete(manager.cleanup, equipmentID)
				}
				manager.mu.Unlock()
			}
		}
		manager.operationMu.Unlock()
		if !owned || err == nil || manager.ctx.Err() != nil {
			return
		}
		select {
		case <-manager.ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (manager *Manager) stop(_ context.Context, request agentlink.ModemDataRequest) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	manager.mu.Lock()
	current := manager.items[request.EquipmentID]
	if current == nil {
		current = manager.cleanup[request.EquipmentID]
	}
	if current != nil && (current.id != request.SessionID || current.target != targetFor(request)) {
		manager.mu.Unlock()
		return errors.New("data session target does not match current owner")
	}
	if current != nil {
		delete(manager.items, request.EquipmentID)
		manager.cleanup[request.EquipmentID] = current
	}
	manager.mu.Unlock()
	if current == nil {
		return nil
	}
	current.cancel()
	err := manager.closeSession(current)
	if err == nil {
		manager.mu.Lock()
		if manager.cleanup[request.EquipmentID] == current {
			delete(manager.cleanup, request.EquipmentID)
		}
		manager.mu.Unlock()
	}
	return err
}

// DoAuxiliary serializes a call/raw/SIM operation with data lifecycle changes
// and rejects it while the exact modem still owns a prepared or open data
// session. Transport-loss cleanup deliberately bypasses this method and calls
// Manager.Close so a broken WSS can never strand a raw USB attachment.
func (manager *Manager) DoAuxiliary(ctx context.Context, equipmentID string, callback func(context.Context) error) error {
	if callback == nil {
		return errors.New("nil modem auxiliary operation")
	}
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	manager.mu.Lock()
	active := manager.items[equipmentID] != nil || manager.cleanup[equipmentID] != nil
	closed := manager.closed
	manager.mu.Unlock()
	if active {
		return ErrSessionActive
	}
	if closed {
		return context.Canceled
	}
	return callback(ctx)
}

func (manager *Manager) closeSession(current *dataSession) error {
	manager.closeStreams(current)
	current.stopMu.Lock()
	defer current.stopMu.Unlock()
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	current.stopErr = manager.config.Backend.StopData(stopCtx, current.target)
	cancel()
	return current.stopErr
}

func (manager *Manager) closeStreams(current *dataSession) {
	current.mu.Lock()
	streams := make([]net.Conn, 0, len(current.streams))
	for _, conn := range current.streams {
		streams = append(streams, conn)
	}
	current.streams = map[string]net.Conn{}
	current.mu.Unlock()
	for _, conn := range streams {
		_ = conn.Close()
	}
}

func (manager *Manager) Close() error {
	manager.operationMu.Lock()
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		manager.operationMu.Unlock()
		return nil
	}
	manager.closed = true
	manager.cancel()
	items := make([]*dataSession, 0, len(manager.items))
	for equipmentID, item := range manager.items {
		items = append(items, item)
		manager.cleanup[equipmentID] = item
	}
	for _, item := range manager.cleanup {
		if !slices.Contains(items, item) {
			items = append(items, item)
		}
	}
	manager.items = map[string]*dataSession{}
	manager.mu.Unlock()
	var failures []error
	for _, item := range items {
		item.cancel()
		if err := manager.closeSession(item); err != nil {
			failures = append(failures, err)
		} else {
			manager.mu.Lock()
			if manager.cleanup[item.target.EquipmentID] == item {
				delete(manager.cleanup, item.target.EquipmentID)
			}
			manager.mu.Unlock()
		}
	}
	manager.operationMu.Unlock()
	manager.wait.Wait()
	return errors.Join(failures...)
}

func targetFor(request agentlink.ModemDataRequest) Target {
	return Target{AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID, CardID: request.CardID}
}

func dataFailure(err error) *agentlink.RemoteError {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &agentlink.RemoteError{Kind: "transport", Code: "modem_data_timeout", Retryable: true}
	case strings.Contains(err.Error(), "owns"), strings.Contains(err.Error(), "replaced"):
		return &agentlink.RemoteError{Kind: "conflict", Code: "modem_data_session_conflict"}
	case strings.Contains(err.Error(), "quota"):
		return &agentlink.RemoteError{Kind: "rejected", Code: "modem_data_quota_exhausted"}
	default:
		return &agentlink.RemoteError{Kind: "failed", Code: "modem_data_failed", Retryable: true}
	}
}

func agentDataURL(serverURL string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	parsed.Path, parsed.RawQuery, parsed.Fragment = "/v1/agent/data/ws", "", ""
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else if parsed.Scheme == "http" {
		parsed.Scheme = "ws"
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return "", errors.New("invalid Agent data server URL")
	}
	return parsed.String(), nil
}
