// Package agentusbip pairs one exporting Agent and one importing Agent over
// the existing authenticated HTTPS/WSS listener. sing-mux multiplexes all
// USB/IP logical connections inside this one paired WSS byte stream.
package agentusbip

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const (
	defaultCapacity = 128
	maximumMessage  = 17 << 20
)

type Role string

const (
	RoleExporter Role = "exporter"
	RoleImporter Role = "importer"
)

var ErrReservationNotFound = errors.New("Agent USB/IP reservation was not found")

// SessionIdentity is the authoritative source modem and inserted SIM. Bus IDs
// are transport-local and deliberately absent from the durable identity.
type SessionIdentity struct {
	SourceAgentID           string
	SourceProcessGeneration string
	AttachmentID            string
	SessionGeneration       string
	EquipmentID             string
	CardID                  string
	USBSessionID            string
	StreamID                string
}

// EndpointIdentity adds the currently connecting endpoint. The importer is a
// separate root Agent on the service host; it can never impersonate the source.
type EndpointIdentity struct {
	SessionIdentity
	Role              Role
	AgentID           string
	ProcessGeneration string
	StreamToken       string
}

type Reservation struct {
	SessionIdentity
	ImporterAgentID           string
	ImporterProcessGeneration string
	ExporterStreamToken       string
	ImporterStreamToken       string
	ExpiresAt                 time.Time
}

type Broker struct {
	tokens   agentlink.TokenResolver
	now      func() time.Time
	capacity int

	mu    sync.Mutex
	items map[string]*reservation
}

type reservation struct {
	Reservation
	exporterTokenHash [sha256.Size]byte
	importerTokenHash [sha256.Size]byte
	paired            chan struct{}
	done              chan struct{}
	exporter          *endpoint
	importer          *endpoint
	pairedEndpoints   bool
	active            bool
	timer             *time.Timer
	closeOnce         sync.Once
}

type endpoint struct {
	socket *websocket.Conn
	acked  chan struct{}
}

func NewBroker(tokens agentlink.TokenResolver, now func() time.Time, capacity int) (*Broker, error) {
	if tokens == nil {
		return nil, errors.New("Agent USB/IP token resolver is required")
	}
	if now == nil {
		now = time.Now
	}
	if capacity == 0 {
		capacity = defaultCapacity
	}
	if capacity < 1 || capacity > 4096 {
		return nil, errors.New("Agent USB/IP capacity must be between 1 and 4096")
	}
	return &Broker{tokens: tokens, now: now, capacity: capacity, items: make(map[string]*reservation)}, nil
}

func NewStreamToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (broker *Broker) Reserve(input Reservation) error {
	normalizeSession(&input.SessionIdentity)
	input.ImporterAgentID = strings.TrimSpace(input.ImporterAgentID)
	input.ImporterProcessGeneration = strings.TrimSpace(input.ImporterProcessGeneration)
	input.ExpiresAt = input.ExpiresAt.UTC()
	now := broker.now().UTC()
	if validateSession(input.SessionIdentity) != nil || !validID(input.ImporterAgentID) ||
		!validID(input.ImporterProcessGeneration) || !validStreamToken(input.ExporterStreamToken) ||
		!validStreamToken(input.ImporterStreamToken) || input.ExporterStreamToken == input.ImporterStreamToken ||
		!input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(2*time.Minute)) {
		return errors.New("invalid Agent USB/IP reservation")
	}
	broker.purge(now)
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if _, exists := broker.items[input.StreamID]; exists {
		return errors.New("Agent USB/IP stream is already reserved")
	}
	if len(broker.items) >= broker.capacity {
		return errors.New("Agent USB/IP reservation capacity is exhausted")
	}
	exporterTokenHash := sha256.Sum256([]byte(input.ExporterStreamToken))
	importerTokenHash := sha256.Sum256([]byte(input.ImporterStreamToken))
	input.ExporterStreamToken, input.ImporterStreamToken = "", ""
	record := &reservation{
		Reservation: input, exporterTokenHash: exporterTokenHash, importerTokenHash: importerTokenHash,
		paired: make(chan struct{}), done: make(chan struct{}),
	}
	// Arm the autonomous handshake deadline before publishing the record.
	// The callback blocks on broker.mu until the record is visible, which also
	// prevents Revoke from racing with assignment of record.timer.
	record.timer = time.AfterFunc(input.ExpiresAt.Sub(now), func() { broker.expire(input.StreamID, record) })
	broker.items[input.StreamID] = record
	return nil
}

func (broker *Broker) Revoke(streamID string) {
	streamID = strings.TrimSpace(streamID)
	broker.mu.Lock()
	record := broker.items[streamID]
	delete(broker.items, streamID)
	broker.mu.Unlock()
	closeReservation(record)
}

func (broker *Broker) RevokeSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	var records []*reservation
	broker.mu.Lock()
	for streamID, record := range broker.items {
		if record.USBSessionID == sessionID {
			delete(broker.items, streamID)
			records = append(records, record)
		}
	}
	broker.mu.Unlock()
	for _, record := range records {
		closeReservation(record)
	}
}

func (broker *Broker) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/agent/usbip/ws" || request.Method != http.MethodGet || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	identity := endpointFromHeaders(request.Header)
	serverToken, err := broker.tokens.TokenForAgent(request.Context(), identity.AgentID)
	if err != nil || !bearerEqual(request.Header.Get("Authorization"), serverToken) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	broker.purge(broker.now().UTC())
	broker.mu.Lock()
	record := broker.items[identity.StreamID]
	valid := record != nil && record.endpointAllowed(identity) &&
		record.tokenMatches(identity.Role, request.Header.Get("X-MDD-USBIP-Token")) && record.endpoint(identity.Role) == nil
	broker.mu.Unlock()
	if !valid {
		http.Error(response, "usbip_reservation_mismatch", http.StatusConflict)
		return
	}
	socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	socket.SetReadLimit(maximumMessage)
	current := &endpoint{socket: socket, acked: make(chan struct{})}
	broker.mu.Lock()
	record = broker.items[identity.StreamID]
	if record == nil || !record.endpointAllowed(identity) || record.endpoint(identity.Role) != nil ||
		!record.tokenMatches(identity.Role, request.Header.Get("X-MDD-USBIP-Token")) {
		broker.mu.Unlock()
		socket.CloseNow()
		return
	}
	record.setEndpoint(identity.Role, current)
	if record.exporter != nil && record.importer != nil && !record.pairedEndpoints {
		record.pairedEndpoints = true
		close(record.paired)
		go broker.bridge(identity.StreamID, record)
	}
	paired, done := record.paired, record.done
	broker.mu.Unlock()

	select {
	case <-paired:
	case <-request.Context().Done():
		broker.remove(identity.StreamID, record)
		return
	case <-done:
		return
	}
	acknowledgement, _ := json.Marshal(map[string]any{
		"type": "agent.usbip.ready", "version": 1, "stream_id": identity.StreamID, "role": identity.Role,
	})
	ackContext, cancelAck := context.WithDeadline(request.Context(), record.ExpiresAt)
	err = socket.Write(ackContext, websocket.MessageText, acknowledgement)
	cancelAck()
	if err != nil {
		broker.remove(identity.StreamID, record)
		return
	}
	close(current.acked)
	broker.markActive(identity.StreamID, record)
	select {
	case <-request.Context().Done():
		broker.remove(identity.StreamID, record)
	case <-done:
	}
}

func (broker *Broker) bridge(streamID string, record *reservation) {
	if !waitForAck(record.exporter, record.done) || !waitForAck(record.importer, record.done) {
		broker.remove(streamID, record)
		return
	}
	exporter := websocket.NetConn(context.Background(), record.exporter.socket, websocket.MessageBinary)
	importer := websocket.NetConn(context.Background(), record.importer.socket, websocket.MessageBinary)
	results := make(chan error, 2)
	go func() { _, err := io.Copy(exporter, importer); results <- err }()
	go func() { _, err := io.Copy(importer, exporter); results <- err }()
	<-results
	_ = exporter.Close()
	_ = importer.Close()
	<-results
	broker.remove(streamID, record)
}

func waitForAck(current *endpoint, done <-chan struct{}) bool {
	select {
	case <-current.acked:
		return true
	case <-done:
		return false
	}
}

func (broker *Broker) remove(streamID string, expected *reservation) {
	broker.mu.Lock()
	if broker.items[streamID] == expected {
		delete(broker.items, streamID)
	}
	broker.mu.Unlock()
	closeReservation(expected)
}

func (broker *Broker) purge(now time.Time) {
	var records []*reservation
	broker.mu.Lock()
	for streamID, record := range broker.items {
		// ExpiresAt bounds endpoint pairing and both acknowledgements. Only a
		// fully acknowledged transport becomes long-lived.
		if !record.active && !record.ExpiresAt.After(now) {
			delete(broker.items, streamID)
			records = append(records, record)
		}
	}
	broker.mu.Unlock()
	for _, record := range records {
		closeReservation(record)
	}
}

func (broker *Broker) expire(streamID string, expected *reservation) {
	broker.mu.Lock()
	if broker.items[streamID] == expected && !expected.active {
		delete(broker.items, streamID)
	} else {
		expected = nil
	}
	broker.mu.Unlock()
	closeReservation(expected)
}

func (broker *Broker) markActive(streamID string, expected *reservation) {
	broker.mu.Lock()
	if broker.items[streamID] == expected && !expected.active &&
		expected.exporter != nil && expected.importer != nil &&
		channelClosed(expected.exporter.acked) && channelClosed(expected.importer.acked) {
		expected.active = true
		if expected.timer != nil {
			expected.timer.Stop()
		}
	}
	broker.mu.Unlock()
}

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func closeReservation(record *reservation) {
	if record == nil {
		return
	}
	record.closeOnce.Do(func() {
		if record.timer != nil {
			record.timer.Stop()
		}
		if record.exporter != nil {
			record.exporter.socket.CloseNow()
		}
		if record.importer != nil {
			record.importer.socket.CloseNow()
		}
		close(record.done)
	})
}

func (record *reservation) tokenMatches(role Role, token string) bool {
	switch role {
	case RoleExporter:
		return hashEqual(record.exporterTokenHash, token)
	case RoleImporter:
		return hashEqual(record.importerTokenHash, token)
	default:
		return false
	}
}

func (record *reservation) endpoint(role Role) *endpoint {
	if role == RoleExporter {
		return record.exporter
	}
	if role == RoleImporter {
		return record.importer
	}
	return nil
}

func (record *reservation) setEndpoint(role Role, current *endpoint) {
	if role == RoleExporter {
		record.exporter = current
	} else {
		record.importer = current
	}
}

func (record *reservation) endpointAllowed(identity EndpointIdentity) bool {
	if validateEndpoint(identity) != nil || identity.SessionIdentity != record.SessionIdentity {
		return false
	}
	switch identity.Role {
	case RoleExporter:
		return identity.AgentID == record.SourceAgentID && identity.ProcessGeneration == record.SourceProcessGeneration
	case RoleImporter:
		return identity.AgentID == record.ImporterAgentID && identity.ProcessGeneration == record.ImporterProcessGeneration
	default:
		return false
	}
}

func normalizeSession(identity *SessionIdentity) {
	identity.SourceAgentID = strings.TrimSpace(identity.SourceAgentID)
	identity.SourceProcessGeneration = strings.TrimSpace(identity.SourceProcessGeneration)
	identity.AttachmentID = strings.TrimSpace(identity.AttachmentID)
	identity.SessionGeneration = strings.TrimSpace(identity.SessionGeneration)
	identity.EquipmentID = strings.TrimSpace(identity.EquipmentID)
	identity.CardID = strings.TrimSpace(identity.CardID)
	identity.USBSessionID = strings.TrimSpace(identity.USBSessionID)
	identity.StreamID = strings.TrimSpace(identity.StreamID)
}

func normalizeEndpoint(identity *EndpointIdentity) {
	normalizeSession(&identity.SessionIdentity)
	identity.AgentID = strings.TrimSpace(identity.AgentID)
	identity.ProcessGeneration = strings.TrimSpace(identity.ProcessGeneration)
}

func validateSession(identity SessionIdentity) error {
	for _, value := range []string{identity.SourceAgentID, identity.SourceProcessGeneration, identity.AttachmentID,
		identity.SessionGeneration, identity.EquipmentID, identity.USBSessionID, identity.StreamID} {
		if !validID(value) {
			return errors.New("invalid Agent USB/IP session identity")
		}
	}
	if !digits(identity.CardID, 4, 32) {
		return errors.New("invalid Agent USB/IP card identity")
	}
	return nil
}

func validateEndpoint(identity EndpointIdentity) error {
	if validateSession(identity.SessionIdentity) != nil || !validID(identity.AgentID) || !validID(identity.ProcessGeneration) ||
		(identity.Role != RoleExporter && identity.Role != RoleImporter) || len(identity.StreamToken) < 32 || len(identity.StreamToken) > 512 {
		return errors.New("invalid Agent USB/IP endpoint identity")
	}
	return nil
}

func endpointFromHeaders(header http.Header) EndpointIdentity {
	return EndpointIdentity{
		SessionIdentity: SessionIdentity{
			SourceAgentID:           header.Get("X-MDD-USBIP-Source-Agent"),
			SourceProcessGeneration: header.Get("X-MDD-USBIP-Source-Generation"),
			AttachmentID:            header.Get("X-MDD-USBIP-Attachment"),
			SessionGeneration:       header.Get("X-MDD-USBIP-Card-Generation"),
			EquipmentID:             header.Get("X-MDD-USBIP-Equipment"), CardID: header.Get("X-MDD-USBIP-Card"),
			USBSessionID: header.Get("X-MDD-USBIP-Session"), StreamID: header.Get("X-MDD-USBIP-Stream"),
		},
		Role: Role(header.Get("X-MDD-USBIP-Role")), AgentID: header.Get("X-MDD-Agent-ID"),
		ProcessGeneration: header.Get("X-MDD-Agent-Generation"),
		StreamToken:       header.Get("X-MDD-USBIP-Token"),
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

func validStreamToken(value string) bool { return len(value) >= 32 && len(value) <= 512 }

func validID(value string) bool {
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

func digits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
