// Package agentdata transports explicit cellular borrowing flows over the
// Agent's existing HTTPS/WSS listener. Each TCP or UDP flow has an independent
// WebSocket so one blocked flow cannot stall control, health, or another flow.
package agentdata

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const maximumDatagram = 65535

var (
	ErrReservationNotFound = errors.New("Agent data reservation was not found")
	ErrDataNotReady        = errors.New("Agent data stream is not ready")
)

type Reservation struct {
	AgentID           string
	ProcessGeneration string
	SessionID         string
	StreamID          string
	StreamToken       string
	Network           string
	ExpiresAt         time.Time
}

type Broker struct {
	tokens agentlink.TokenResolver
	now    func() time.Time
	mu     sync.Mutex
	items  map[string]*reservation
}

type reservation struct {
	Reservation
	tokenHash [sha256.Size]byte
	ready     chan struct{}
	conn      *trackedConn
	claimed   bool
}

func NewBroker(tokens agentlink.TokenResolver, now func() time.Time) (*Broker, error) {
	if tokens == nil {
		return nil, errors.New("Agent data token resolver is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Broker{tokens: tokens, now: now, items: map[string]*reservation{}}, nil
}

func (broker *Broker) Reserve(input Reservation) error {
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.ProcessGeneration = strings.TrimSpace(input.ProcessGeneration)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.StreamID = strings.TrimSpace(input.StreamID)
	input.ExpiresAt = input.ExpiresAt.UTC()
	now := broker.now().UTC()
	if input.AgentID == "" || input.ProcessGeneration == "" || input.SessionID == "" || input.StreamID == "" ||
		(input.Network != "tcp" && input.Network != "udp") || len(input.StreamToken) < 32 || len(input.StreamToken) > 512 ||
		!input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(25*time.Hour)) {
		return errors.New("invalid Agent data reservation")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.purgeLocked(now)
	if _, exists := broker.items[input.StreamID]; exists {
		return errors.New("Agent data stream is already reserved")
	}
	if len(broker.items) >= 4096 {
		return errors.New("Agent data reservation capacity is exhausted")
	}
	broker.items[input.StreamID] = &reservation{Reservation: input,
		tokenHash: sha256.Sum256([]byte(input.StreamToken)), ready: make(chan struct{})}
	return nil
}

// RenewSession extends every current reservation for one exact data session.
// It never creates a reservation and never shortens an existing deadline.
// Core calls this after the Agent has accepted the same session renewal, so a
// later purge cannot close streams that belong to the renewed lease.
func (broker *Broker) RenewSession(sessionID string, expiresAt time.Time) error {
	sessionID = strings.TrimSpace(sessionID)
	expiresAt = expiresAt.UTC()
	now := broker.now().UTC()
	if sessionID == "" || !expiresAt.After(now) || expiresAt.After(now.Add(25*time.Hour)) {
		return errors.New("invalid Agent data session renewal")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.purgeLocked(now)
	for _, record := range broker.items {
		if record.SessionID == sessionID && expiresAt.Before(record.ExpiresAt) {
			return errors.New("Agent data session renewal cannot shorten a stream")
		}
	}
	for _, record := range broker.items {
		if record.SessionID == sessionID {
			record.ExpiresAt = expiresAt
		}
	}
	return nil
}

func (broker *Broker) Acquire(ctx context.Context, streamID string) (net.Conn, error) {
	broker.mu.Lock()
	broker.purgeLocked(broker.now().UTC())
	record := broker.items[strings.TrimSpace(streamID)]
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
	record = broker.items[strings.TrimSpace(streamID)]
	if record == nil || record.conn == nil || record.claimed {
		return nil, ErrDataNotReady
	}
	record.claimed = true
	return record.conn, nil
}

func (broker *Broker) Revoke(streamID string) {
	broker.mu.Lock()
	record := broker.items[strings.TrimSpace(streamID)]
	delete(broker.items, strings.TrimSpace(streamID))
	broker.mu.Unlock()
	if record != nil && record.conn != nil {
		_ = record.conn.Close()
	}
}

func (broker *Broker) RevokeSession(sessionID string) {
	var conns []*trackedConn
	broker.mu.Lock()
	for key, record := range broker.items {
		if record.SessionID == sessionID {
			delete(broker.items, key)
			if record.conn != nil {
				conns = append(conns, record.conn)
			}
		}
	}
	broker.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (broker *Broker) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/agent/data/ws" || request.Method != http.MethodGet || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	agentID := strings.TrimSpace(request.Header.Get("X-MDD-Agent-ID"))
	generation := strings.TrimSpace(request.Header.Get("X-MDD-Agent-Generation"))
	streamID := strings.TrimSpace(request.Header.Get("X-MDD-Data-Stream"))
	token, err := broker.tokens.TokenForAgent(request.Context(), agentID)
	if err != nil || !bearerEqual(request.Header.Get("Authorization"), token) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	broker.mu.Lock()
	broker.purgeLocked(broker.now().UTC())
	record := broker.items[streamID]
	valid := record != nil && record.AgentID == agentID && record.ProcessGeneration == generation &&
		record.conn == nil && hashEqual(record.tokenHash, request.Header.Get("X-MDD-Data-Token"))
	broker.mu.Unlock()
	if !valid {
		http.Error(response, "data_reservation_mismatch", http.StatusConflict)
		return
	}
	socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	socket.SetReadLimit(maximumDatagram)
	var raw net.Conn
	if record.Network == "tcp" {
		raw = websocket.NetConn(context.Background(), socket, websocket.MessageBinary)
	} else {
		raw = newPacketConn(socket)
	}
	conn := newTrackedConn(raw)
	broker.mu.Lock()
	record = broker.items[streamID]
	if record == nil || record.conn != nil || record.AgentID != agentID || record.ProcessGeneration != generation ||
		!hashEqual(record.tokenHash, request.Header.Get("X-MDD-Data-Token")) {
		broker.mu.Unlock()
		_ = conn.Close()
		return
	}
	record.conn = conn
	broker.mu.Unlock()
	ack, _ := json.Marshal(map[string]any{"type": "agent.data.ready", "version": 1, "stream_id": streamID})
	if err := socket.Write(request.Context(), websocket.MessageText, ack); err != nil {
		broker.Revoke(streamID)
		return
	}
	broker.mu.Lock()
	if broker.items[streamID] != record || record.conn != conn {
		broker.mu.Unlock()
		_ = conn.Close()
		return
	}
	close(record.ready)
	broker.mu.Unlock()
	select {
	case <-request.Context().Done():
		_ = conn.Close()
	case <-conn.done:
	}
}

func (broker *Broker) purgeLocked(now time.Time) {
	for key, record := range broker.items {
		if !record.ExpiresAt.After(now) {
			delete(broker.items, key)
			if record.conn != nil {
				_ = record.conn.Close()
			}
		}
	}
}

func bearerEqual(header, token string) bool {
	const prefix = "Bearer "
	return len(token) >= 32 && strings.HasPrefix(header, prefix) &&
		subtle.ConstantTimeCompare([]byte(strings.TrimSpace(header[len(prefix):])), []byte(token)) == 1
}

func hashEqual(expected [sha256.Size]byte, value string) bool {
	actual := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(expected[:], actual[:]) == 1
}

type trackedConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func newTrackedConn(conn net.Conn) *trackedConn {
	return &trackedConn{Conn: conn, done: make(chan struct{})}
}

func (conn *trackedConn) Close() error {
	var err error
	conn.once.Do(func() { err = conn.Conn.Close(); close(conn.done) })
	return err
}
