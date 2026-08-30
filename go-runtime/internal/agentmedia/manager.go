// Package agentmedia owns the Agent side of one modem PCM session. The media
// WebSocket is outbound, uses Core's existing public listener, and remains
// separate from the Agent control connection so PCM backpressure cannot delay
// health, lease renewal, or hangup commands.
package agentmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

const (
	pcmFrameBytes  = 320
	pcmWriteBytes  = 640 // Quectel serial PCM contract: 40 ms at 8 kHz S16 mono
	maximumAckSize = 4096
)

type Config struct {
	Context           context.Context
	ServerURL         string
	ServerToken       string
	AgentID           string
	ProcessGeneration string
	HTTPClient        *http.Client
	Endpoints         agentmodem.MediaOperator
}

type Manager struct {
	config Config
	url    string
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
	wait     sync.WaitGroup
}

type session struct {
	request   agentlink.ModemMediaRequest
	tokenHash [sha256.Size]byte
	endpoint  io.ReadWriteCloser
	socket    *websocket.Conn
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

func (current *session) closeTransport() {
	current.closeOnce.Do(func() {
		current.cancel()
		current.socket.CloseNow()
		_ = current.endpoint.Close()
	})
}

func NewManager(config Config) (*Manager, error) {
	if config.Context == nil || config.HTTPClient == nil || config.Endpoints == nil ||
		len(config.ServerToken) < 32 || strings.TrimSpace(config.AgentID) == "" ||
		strings.TrimSpace(config.ProcessGeneration) == "" {
		return nil, errors.New("invalid Agent media configuration")
	}
	mediaURL, err := mediaURL(config.ServerURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(config.Context)
	return &Manager{config: config, url: mediaURL, ctx: ctx, cancel: cancel, sessions: map[string]*session{}}, nil
}

func (manager *Manager) ExecuteModemMedia(ctx context.Context, request agentlink.ModemMediaRequest) agentlink.ModemMediaResponse {
	response := agentlink.ModemMediaResponse{
		OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID, SessionID: request.SessionID,
	}
	if err := request.Validate(); err != nil {
		response.Failure = &agentlink.RemoteError{Kind: "rejected", Code: "invalid_modem_media_request"}
		return response
	}
	var err error
	if request.Action == agentlink.ModemMediaPrepare {
		err = manager.prepare(ctx, request)
		if err == nil {
			response.State = "ready"
			return response
		}
	} else {
		err = manager.stop(ctx, request)
		if err == nil {
			response.State = "stopped"
			return response
		}
	}
	response.Failure = mediaFailure(err)
	return response
}

func (manager *Manager) prepare(ctx context.Context, request agentlink.ModemMediaRequest) error {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return context.Canceled
	}
	if current := manager.sessions[request.EquipmentID]; current != nil {
		same := sameTarget(current.request, request) && tokenMatches(current.tokenHash, request.MediaToken)
		manager.mu.Unlock()
		if same {
			return nil
		}
		return errors.New("another media session owns this modem")
	}
	manager.mu.Unlock()

	endpoint, err := manager.config.Endpoints.OpenVoicePCM(ctx, agentmodem.MediaTarget{
		AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID, CardID: request.CardID,
	})
	if err != nil {
		return fmt.Errorf("open exact modem PCM: %w", err)
	}
	socket, err := manager.dial(ctx, request)
	if err != nil {
		_ = endpoint.Close()
		return err
	}
	sessionContext, cancel := context.WithCancel(manager.ctx)
	current := &session{
		request: request, tokenHash: sha256.Sum256([]byte(request.MediaToken)),
		endpoint: endpoint, socket: socket, cancel: cancel, done: make(chan struct{}),
	}
	manager.mu.Lock()
	if manager.closed || manager.sessions[request.EquipmentID] != nil {
		manager.mu.Unlock()
		cancel()
		socket.CloseNow()
		_ = endpoint.Close()
		return errors.New("media session ownership changed during preparation")
	}
	manager.sessions[request.EquipmentID] = current
	manager.wait.Add(1)
	manager.mu.Unlock()
	go manager.run(sessionContext, current)
	return nil
}

func (manager *Manager) dial(ctx context.Context, request agentlink.ModemMediaRequest) (*websocket.Conn, error) {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+manager.config.ServerToken)
	headers.Set("X-MDD-Agent-ID", manager.config.AgentID)
	headers.Set("X-MDD-Agent-Generation", manager.config.ProcessGeneration)
	headers.Set("X-MDD-Media-Session", request.SessionID)
	headers.Set("X-MDD-Media-Token", request.MediaToken)
	socket, response, err := websocket.Dial(ctx, manager.url, &websocket.DialOptions{
		HTTPClient: manager.config.HTTPClient, HTTPHeader: headers,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("connect Agent media WSS: HTTP %d: %w", response.StatusCode, err)
		}
		return nil, fmt.Errorf("connect Agent media WSS: %w", err)
	}
	ackContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	messageType, payload, err := socket.Read(ackContext)
	cancel()
	if err != nil || messageType != websocket.MessageText || len(payload) > maximumAckSize || !validAck(payload, request.SessionID) {
		_ = socket.Close(websocket.StatusPolicyViolation, "invalid media acknowledgement")
		return nil, errors.New("Core did not acknowledge the exact Agent media session")
	}
	socket.SetReadLimit(pcmFrameBytes)
	return socket, nil
}

func (manager *Manager) run(ctx context.Context, current *session) {
	defer manager.wait.Done()
	defer close(current.done)
	defer current.closeTransport()
	results := make(chan error, 2)
	go func() { results <- sendDownlink(ctx, current.endpoint, current.socket) }()
	go func() { results <- receiveUplink(ctx, current.socket, current.endpoint) }()
	<-results
	current.closeTransport()
	<-results
	manager.mu.Lock()
	if manager.sessions[current.request.EquipmentID] == current {
		delete(manager.sessions, current.request.EquipmentID)
	}
	manager.mu.Unlock()
}

func (manager *Manager) stop(ctx context.Context, request agentlink.ModemMediaRequest) error {
	manager.mu.Lock()
	current := manager.sessions[request.EquipmentID]
	if current == nil {
		manager.mu.Unlock()
		return nil
	}
	if !sameTarget(current.request, request) {
		manager.mu.Unlock()
		return errors.New("media session target does not match current owner")
	}
	done := current.done
	manager.mu.Unlock()
	current.closeTransport()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
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
	for _, current := range sessions {
		current.closeTransport()
	}
	manager.wait.Wait()
	return nil
}

func sendDownlink(ctx context.Context, endpoint io.Reader, socket *websocket.Conn) error {
	frame := make([]byte, pcmFrameBytes)
	for {
		if err := readExact(ctx, endpoint, frame); err != nil {
			return err
		}
		if err := socket.Write(ctx, websocket.MessageBinary, frame); err != nil {
			return err
		}
	}
}

func receiveUplink(ctx context.Context, socket *websocket.Conn, endpoint io.Writer) error {
	packet := make([]byte, 0, pcmWriteBytes)
	for {
		messageType, payload, err := socket.Read(ctx)
		if err != nil {
			return err
		}
		if messageType != websocket.MessageBinary || len(payload) != pcmFrameBytes {
			return errors.New("Agent media uplink must contain exact 20 ms PCM frames")
		}
		packet = append(packet, payload...)
		if len(packet) == pcmWriteBytes {
			if err := writeExact(endpoint, packet); err != nil {
				return err
			}
			packet = packet[:0]
		}
	}
}

func readExact(ctx context.Context, source io.Reader, target []byte) error {
	offset := 0
	for offset < len(target) {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := source.Read(target[offset:])
		if count > 0 {
			offset += count
		}
		if err != nil {
			return err
		}
		if count == 0 {
			timer := time.NewTimer(time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}

func writeExact(destination io.Writer, payload []byte) error {
	for len(payload) != 0 {
		count, err := destination.Write(payload)
		if count > 0 {
			payload = payload[count:]
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func validAck(payload []byte, sessionID string) bool {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var ack struct {
		Type      string `json:"type"`
		Version   int    `json:"version"`
		SessionID string `json:"session_id"`
	}
	if decoder.Decode(&ack) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	return ack.Type == "agent.media.ready" && ack.Version == 1 && ack.SessionID == sessionID
}

func mediaURL(serverURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!strings.HasSuffix(parsed.Path, "/v1/agent/ws") {
		return "", errors.New("Agent server URL cannot derive the media WSS endpoint")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/v1/agent/ws") + "/v1/agent/media/ws"
	return parsed.String(), nil
}

func sameTarget(left, right agentlink.ModemMediaRequest) bool {
	return left.AttachmentID == right.AttachmentID && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID && left.SessionID == right.SessionID
}

func tokenMatches(expected [sha256.Size]byte, presented string) bool {
	actual := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(expected[:], actual[:]) == 1
}

func mediaFailure(err error) *agentlink.RemoteError {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &agentlink.RemoteError{Kind: "transport", Code: "modem_media_timeout", Retryable: true}
	case errors.Is(err, agentmodem.ErrOperationTargetReplaced):
		return &agentlink.RemoteError{Kind: "conflict", Code: "modem_target_replaced"}
	case errors.Is(err, agentmodem.ErrOperationUnavailable):
		return &agentlink.RemoteError{Kind: "not_ready", Code: "modem_pcm_unavailable", Retryable: true}
	case strings.Contains(err.Error(), "owns this modem"), strings.Contains(err.Error(), "current owner"),
		strings.Contains(err.Error(), "ownership changed"):
		return &agentlink.RemoteError{Kind: "conflict", Code: "modem_media_conflict"}
	default:
		return &agentlink.RemoteError{Kind: "failed", Code: "modem_media_failed", Retryable: true}
	}
}
