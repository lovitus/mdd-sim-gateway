package agentmedia

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const (
	defaultBrokerCapacity = 64
	maximumQueuedFrames   = 100 // 2 seconds of 20 ms PCM, bounded per session.
)

var (
	ErrReservationNotFound = errors.New("Agent media reservation was not found")
	ErrMediaNotReady       = errors.New("Agent media is not ready")
	ErrMediaAlreadyClaimed = errors.New("Agent media is already claimed")
)

type Reservation struct {
	AgentID           string
	ProcessGeneration string
	SessionID         string
	MediaToken        string
	ExpiresAt         time.Time
}

type Broker struct {
	tokens   agentlink.TokenResolver
	now      func() time.Time
	capacity int

	mu           sync.Mutex
	reservations map[string]*reservation
}

type reservation struct {
	Reservation
	tokenHash [sha256.Size]byte
	ready     chan struct{}
	peer      *Peer
	claimed   bool
}

// Peer is Core's framed view of the Agent media connection. Broker owns the
// sole WebSocket reader; a later browser session owns the sole framed writer.
type Peer struct {
	socket   *websocket.Conn
	incoming chan []byte
	done     chan struct{}
	writeMu  sync.Mutex
	once     sync.Once
}

func NewBroker(tokens agentlink.TokenResolver, now func() time.Time, capacity int) (*Broker, error) {
	if tokens == nil {
		return nil, errors.New("Agent media token resolver is required")
	}
	if now == nil {
		now = time.Now
	}
	if capacity == 0 {
		capacity = defaultBrokerCapacity
	}
	if capacity < 1 || capacity > 4096 {
		return nil, errors.New("Agent media capacity must be between 1 and 4096")
	}
	return &Broker{tokens: tokens, now: now, capacity: capacity, reservations: map[string]*reservation{}}, nil
}

func NewSessionToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (broker *Broker) Reserve(input Reservation) error {
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.ProcessGeneration = strings.TrimSpace(input.ProcessGeneration)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.ExpiresAt = input.ExpiresAt.UTC()
	now := broker.now().UTC()
	if !brokerID(input.AgentID) || !brokerID(input.ProcessGeneration) || !brokerID(input.SessionID) ||
		len(input.MediaToken) < 32 || len(input.MediaToken) > 512 ||
		input.ExpiresAt.Before(now.Add(time.Second)) || input.ExpiresAt.After(now.Add(2*time.Minute)) {
		return errors.New("invalid Agent media reservation")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.purgeLocked(now)
	if _, exists := broker.reservations[input.SessionID]; exists {
		return errors.New("Agent media session is already reserved")
	}
	if len(broker.reservations) >= broker.capacity {
		return errors.New("Agent media reservation capacity is exhausted")
	}
	broker.reservations[input.SessionID] = &reservation{
		Reservation: input, tokenHash: sha256.Sum256([]byte(input.MediaToken)), ready: make(chan struct{}),
	}
	return nil
}

func (broker *Broker) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/agent/media/ws" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodGet || request.URL.RawQuery != "" {
		http.Error(response, "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	agentID := strings.TrimSpace(request.Header.Get("X-MDD-Agent-ID"))
	generation := strings.TrimSpace(request.Header.Get("X-MDD-Agent-Generation"))
	sessionID := strings.TrimSpace(request.Header.Get("X-MDD-Media-Session"))
	serverToken, err := broker.tokens.TokenForAgent(request.Context(), agentID)
	if err != nil || !bearerEqual(request.Header.Get("Authorization"), serverToken) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	broker.mu.Lock()
	broker.purgeLocked(broker.now().UTC())
	record := broker.reservations[sessionID]
	valid := record != nil && record.AgentID == agentID && record.ProcessGeneration == generation &&
		hashEqual(record.tokenHash, request.Header.Get("X-MDD-Media-Token")) && record.peer == nil
	broker.mu.Unlock()
	if !valid {
		http.Error(response, "media_reservation_mismatch", http.StatusConflict)
		return
	}
	socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	socket.SetReadLimit(pcmFrameBytes)
	peer := &Peer{socket: socket, incoming: make(chan []byte, maximumQueuedFrames), done: make(chan struct{})}
	broker.mu.Lock()
	record = broker.reservations[sessionID]
	if record == nil || record.peer != nil || record.AgentID != agentID || record.ProcessGeneration != generation ||
		!hashEqual(record.tokenHash, request.Header.Get("X-MDD-Media-Token")) {
		broker.mu.Unlock()
		_ = socket.Close(websocket.StatusPolicyViolation, "media reservation changed")
		return
	}
	record.peer = peer
	broker.mu.Unlock()
	ack, _ := json.Marshal(map[string]any{"type": "agent.media.ready", "version": 1, "session_id": sessionID})
	if err := socket.Write(request.Context(), websocket.MessageText, ack); err != nil {
		broker.remove(sessionID, record)
		peer.close()
		return
	}
	broker.mu.Lock()
	if broker.reservations[sessionID] != record || record.peer != peer {
		broker.mu.Unlock()
		peer.close()
		return
	}
	close(record.ready)
	broker.mu.Unlock()
	defer broker.remove(sessionID, record)
	defer peer.close()
	for {
		messageType, payload, err := socket.Read(request.Context())
		if err != nil {
			return
		}
		if messageType != websocket.MessageBinary || len(payload) != pcmFrameBytes {
			_ = socket.Close(websocket.StatusPolicyViolation, "invalid Agent PCM frame")
			return
		}
		frame := append([]byte(nil), payload...)
		select {
		case peer.incoming <- frame:
		case <-peer.done:
			return
		case <-request.Context().Done():
			return
		}
	}
}

func (broker *Broker) Acquire(ctx context.Context, sessionID string) (*Peer, error) {
	broker.mu.Lock()
	broker.purgeLocked(broker.now().UTC())
	record := broker.reservations[strings.TrimSpace(sessionID)]
	if record == nil {
		broker.mu.Unlock()
		return nil, ErrReservationNotFound
	}
	ready := record.ready
	broker.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ready:
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	record = broker.reservations[strings.TrimSpace(sessionID)]
	if record == nil || record.peer == nil {
		return nil, ErrMediaNotReady
	}
	if record.claimed {
		return nil, ErrMediaAlreadyClaimed
	}
	record.claimed = true
	return record.peer, nil
}

func (broker *Broker) Revoke(sessionID string) {
	broker.mu.Lock()
	record := broker.reservations[strings.TrimSpace(sessionID)]
	delete(broker.reservations, strings.TrimSpace(sessionID))
	broker.mu.Unlock()
	if record != nil && record.peer != nil {
		record.peer.close()
	}
}

func (peer *Peer) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-peer.done:
		return nil, ioClosed()
	case frame := <-peer.incoming:
		return frame, nil
	}
}

func (peer *Peer) Write(ctx context.Context, frame []byte) error {
	if len(frame) != pcmFrameBytes {
		return errors.New("Core Agent media writes require exact 20 ms PCM frames")
	}
	peer.writeMu.Lock()
	defer peer.writeMu.Unlock()
	select {
	case <-peer.done:
		return ioClosed()
	default:
	}
	return peer.socket.Write(ctx, websocket.MessageBinary, frame)
}

func (peer *Peer) Done() <-chan struct{} { return peer.done }

// Drain discards frames queued while no browser connection was attached. PCM
// older than the reconnect point must not be replayed as seconds of stale
// audio after a mobile-network interruption.
func (peer *Peer) Drain() {
	for {
		select {
		case <-peer.incoming:
			continue
		default:
			return
		}
	}
}

func (peer *Peer) close() {
	peer.once.Do(func() {
		close(peer.done)
		peer.socket.CloseNow()
	})
}

func (broker *Broker) remove(sessionID string, expected *reservation) {
	broker.mu.Lock()
	if broker.reservations[sessionID] == expected {
		delete(broker.reservations, sessionID)
	}
	broker.mu.Unlock()
}

func (broker *Broker) purgeLocked(now time.Time) {
	for sessionID, record := range broker.reservations {
		// ExpiresAt bounds only the unclaimed preparation bearer. Once the
		// exact Agent has attached, call/session ownership ends it explicitly.
		if record.peer == nil && !record.ExpiresAt.After(now) {
			delete(broker.reservations, sessionID)
		}
	}
}

func bearerEqual(header, token string) bool {
	const prefix = "Bearer "
	return len(token) >= 32 && strings.HasPrefix(header, prefix) &&
		hashEqual(sha256.Sum256([]byte(token)), strings.TrimPrefix(header, prefix))
}

func hashEqual(expected [sha256.Size]byte, value string) bool {
	actual := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(expected[:], actual[:]) == 1
}

func brokerID(value string) bool {
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

func ioClosed() error { return errors.New("Agent media connection is closed") }
