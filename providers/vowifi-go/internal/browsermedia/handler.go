// SPDX-License-Identifier: AGPL-3.0-only

// Package browsermedia terminates Core's same-host media relay inside the
// provider process. The initial session is a no-charge PCM loopback canary;
// attaching it to an IMS MediaCall is a separate explicit paid operation.
package browsermedia

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
)

const (
	PCMFrameBytes      = 320
	defaultMaxSessions = 16
	messageLimit       = 4096
	helloTimeout       = 5 * time.Second
)

type Registry struct {
	mu       sync.Mutex
	token    string
	capacity int
	sessions map[string]*Session
}

// Stream is the narrow live-call media boundary. Signalling and hangup remain
// owned by the service backend; the browser session can only exchange PCM.
type Stream interface {
	WritePCM([]byte, time.Time) (bool, error)
	PCM() <-chan media.PCMFrame
	Errors() <-chan error
}

type Session struct {
	ID       string
	registry *Registry

	mu              sync.Mutex
	socket          *websocket.Conn
	cancel          context.CancelFunc
	streamCancel    context.CancelFunc
	stream          Stream
	connected       bool
	claiming        bool
	protocolReady   bool
	connectionEpoch uint64
	resumeTicket    string
	lastSeen        time.Time
	changes         chan struct{}
	ready           bool
	challenge       string
	inputFrames     uint64
	outputFrames    uint64
	inputSignal     uint64
	outputSignal    uint64
	capture         uint64
	playback        uint64
	played          uint64
}

func NewRegistry(token string, capacity int) (*Registry, error) {
	if len(token) < 32 {
		return nil, errors.New("browser media provider token is invalid")
	}
	if capacity == 0 {
		capacity = defaultMaxSessions
	}
	if capacity < 1 || capacity > 256 {
		return nil, errors.New("browser media capacity must be between 1 and 256")
	}
	return &Registry{token: token, capacity: capacity, sessions: make(map[string]*Session)}, nil
}

func (registry *Registry) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || !literalLoopbackRemote(request.RemoteAddr) {
		http.Error(response, "local_only", http.StatusForbidden)
		return
	}
	if !mediaproxy.AuthorizedToken(request.Header.Get("Authorization"), registry.token) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := strings.TrimPrefix(request.URL.EscapedPath(), "/v1/media/")
	if !validID(id) || strings.Contains(id, "/") || request.URL.RawQuery != "" {
		http.Error(response, "invalid_media_session", http.StatusBadRequest)
		return
	}
	session, reconnecting, err := registry.claim(id)
	if err != nil {
		http.Error(response, "media_session_conflict", http.StatusConflict)
		return
	}
	socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		registry.abandonClaim(session, reconnecting)
		return
	}
	socket.SetReadLimit(messageLimit)
	ctx, cancel := context.WithCancel(context.Background())
	session.attach(socket, cancel)
	defer func() {
		cancel()
		socket.CloseNow()
		registry.releaseConnection(session)
	}()
	if err := session.serve(ctx, socket, reconnecting); err != nil {
		code := websocket.CloseStatus(err)
		if code == -1 {
			code = websocket.StatusPolicyViolation
		}
		closeSocket(socket, code, "browser media session ended")
	}
}

func (registry *Registry) Session(id string) (*Session, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	session, found := registry.sessions[id]
	return session, found
}

func (registry *Registry) CloseAll() {
	registry.mu.Lock()
	sessions := make([]*Session, 0, len(registry.sessions))
	for _, session := range registry.sessions {
		sessions = append(sessions, session)
	}
	registry.mu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

func (registry *Registry) claim(id string) (*Session, bool, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if existing := registry.sessions[id]; existing != nil {
		if !existing.reserveReconnect() {
			return nil, false, errors.New("media session is already claimed")
		}
		return existing, true, nil
	}
	if len(registry.sessions) >= registry.capacity {
		return nil, false, errors.New("media session capacity is exhausted")
	}
	session := &Session{ID: id, registry: registry, claiming: true, changes: make(chan struct{}, 1)}
	registry.sessions[id] = session
	return session, false, nil
}

func (registry *Registry) abandonClaim(session *Session, reconnecting bool) {
	registry.mu.Lock()
	if registry.sessions[session.ID] == session && !reconnecting {
		delete(registry.sessions, session.ID)
	}
	registry.mu.Unlock()
	if reconnecting {
		session.releaseClaim()
	}
}

func (registry *Registry) releaseConnection(session *Session) {
	keep := session.detachSocket()
	if keep {
		return
	}
	registry.mu.Lock()
	if registry.sessions[session.ID] == session {
		delete(registry.sessions, session.ID)
	}
	registry.mu.Unlock()
}

func (registry *Registry) removeSession(session *Session) {
	registry.mu.Lock()
	if registry.sessions[session.ID] == session {
		delete(registry.sessions, session.ID)
	}
	registry.mu.Unlock()
}

func (session *Session) attach(socket *websocket.Conn, cancel context.CancelFunc) {
	session.mu.Lock()
	session.socket, session.cancel = socket, cancel
	session.claiming, session.connected = false, true
	session.connectionEpoch++
	session.signalChangeLocked()
	session.mu.Unlock()
}

func (session *Session) reserveReconnect() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.stream == nil || session.connected || session.claiming {
		return false
	}
	session.claiming = true
	return true
}

func (session *Session) releaseClaim() {
	session.mu.Lock()
	session.claiming = false
	session.mu.Unlock()
}

// detachSocket retains only a session that owns a live paid-call stream so a
// browser may reconnect inside the independent call guard window.
func (session *Session) detachSocket() bool {
	session.mu.Lock()
	if session.streamCancel != nil {
		session.streamCancel()
	}
	session.streamCancel = nil
	session.socket, session.cancel = nil, nil
	session.connected, session.protocolReady, session.claiming = false, false, false
	keep := session.stream != nil
	session.signalChangeLocked()
	session.mu.Unlock()
	return keep
}

func (session *Session) signalChangeLocked() {
	select {
	case session.changes <- struct{}{}:
	default:
	}
}

func (session *Session) touch() {
	session.mu.Lock()
	session.lastSeen = time.Now()
	session.signalChangeLocked()
	session.mu.Unlock()
}

func (session *Session) Connected() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.connected && session.protocolReady
}

func (session *Session) LastSeen() time.Time {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.lastSeen
}

func (session *Session) Changes() <-chan struct{} { return session.changes }

// AttachStream changes the already-proven canary into live call media. It
// fails closed if the browser disappeared between INVITE and attachment.
func (session *Session) AttachStream(stream Stream) error {
	if stream == nil {
		return errors.New("browser media stream is nil")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.ready || !session.connected || !session.protocolReady || session.stream != nil {
		return errors.New("browser media session is not attachable")
	}
	session.stream = stream
	session.startStreamPumpLocked()
	session.signalChangeLocked()
	return nil
}

// EndStream detaches live PCM and closes the browser media session. Signalling
// must already have attempted BYE before the service calls this method.
func (session *Session) EndStream(reason string) {
	session.mu.Lock()
	session.stream = nil
	if session.streamCancel != nil {
		session.streamCancel()
		session.streamCancel = nil
	}
	socket, cancel := session.socket, session.cancel
	session.signalChangeLocked()
	session.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if socket != nil {
		closeSocket(socket, websocket.StatusNormalClosure, reason)
	}
	if session.registry != nil {
		session.registry.removeSession(session)
	}
}

func (session *Session) startStreamPumpLocked() {
	if session.stream == nil || session.socket == nil || !session.protocolReady {
		return
	}
	if session.streamCancel != nil {
		session.streamCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	session.streamCancel = cancel
	stream, socket, epoch := session.stream, session.socket, session.connectionEpoch
	go session.pumpDownlink(ctx, stream, socket, epoch)
}

func (session *Session) pumpDownlink(ctx context.Context, stream Stream, socket *websocket.Conn, epoch uint64) {
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-stream.Errors():
			if ok && err != nil {
				closeSocket(socket, websocket.StatusInternalError, "operator media failed")
			}
			return
		case frame, ok := <-stream.PCM():
			if !ok {
				return
			}
			if len(frame.Data) != PCMFrameBytes || !session.currentSocket(socket, epoch) {
				continue
			}
			if err := socket.Write(ctx, websocket.MessageBinary, frame.Data); err != nil {
				return
			}
		}
	}
}

func (session *Session) currentSocket(socket *websocket.Conn, epoch uint64) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.socket == socket && session.connectionEpoch == epoch && session.connected
}

func (session *Session) close() {
	session.mu.Lock()
	if session.streamCancel != nil {
		session.streamCancel()
		session.streamCancel = nil
	}
	cancel, socket := session.cancel, session.socket
	session.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if socket != nil {
		closeSocket(socket, websocket.StatusGoingAway, "provider shutting down")
	}
}

func closeSocket(socket *websocket.Conn, code websocket.StatusCode, reason string) {
	done := make(chan struct{})
	go func() {
		_ = socket.Close(code, reason)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		socket.CloseNow()
	}
}

func (session *Session) Ready() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.ready
}

func (session *Session) serve(ctx context.Context, socket *websocket.Conn, reconnecting bool) error {
	helloContext, cancel := context.WithTimeout(ctx, helloTimeout)
	kind, payload, err := socket.Read(helloContext)
	cancel()
	if err != nil {
		return err
	}
	var hello helloMessage
	if kind != websocket.MessageText || decodeStrict(payload, &hello) != nil || hello.Version != 1 ||
		hello.SessionID != session.ID || !session.validHello(hello, reconnecting) {
		return errors.New("browser media protocol v1 hello required")
	}
	challenge, err := newChallenge()
	if err != nil {
		return err
	}
	resumeTicket, err := newResumeTicket()
	if err != nil {
		return err
	}
	session.mu.Lock()
	session.challenge, session.resumeTicket = challenge, resumeTicket
	session.lastSeen = time.Now()
	epoch := session.connectionEpoch
	session.signalChangeLocked()
	streamAttached := session.stream != nil
	session.mu.Unlock()
	messageType := "browser.media.claimed"
	if reconnecting {
		messageType = "browser.media.resumed"
	}
	if err := writeJSON(ctx, socket, map[string]any{
		"type": messageType, "version": 1, "challenge": challenge,
		"resume_ticket": resumeTicket, "connection_epoch": epoch,
	}); err != nil {
		return err
	}
	if err := writeJSON(ctx, socket, map[string]any{
		"type": "browser.media.started", "version": 1,
		"purpose": map[bool]string{true: "call", false: "canary"}[streamAttached],
	}); err != nil {
		return err
	}
	session.mu.Lock()
	session.protocolReady = true
	session.startStreamPumpLocked()
	session.signalChangeLocked()
	session.mu.Unlock()
	readySent := false
	for {
		kind, payload, err := socket.Read(ctx)
		if err != nil {
			return err
		}
		session.touch()
		switch kind {
		case websocket.MessageBinary:
			if len(payload) != PCMFrameBytes {
				return errors.New("browser sent an invalid PCM frame")
			}
			stream := session.currentStream()
			if stream != nil {
				if _, err := stream.WritePCM(payload, time.Now()); err != nil {
					return err
				}
			} else {
				session.mu.Lock()
				session.inputFrames++
				if hasSignal(payload) {
					session.inputSignal++
				}
				session.mu.Unlock()
				if err := socket.Write(ctx, websocket.MessageBinary, payload); err != nil {
					return err
				}
				session.mu.Lock()
				session.outputFrames++
				if hasSignal(payload) {
					session.outputSignal++
				}
				session.mu.Unlock()
			}
		case websocket.MessageText:
			var evidence evidenceMessage
			if decodeStrict(payload, &evidence) != nil || evidence.Type != "browser.media.evidence" || evidence.Version != 1 {
				return errors.New("invalid browser media control message")
			}
			if err := session.recordEvidence(evidence); err != nil {
				return err
			}
		default:
			return errors.New("invalid browser media message type")
		}
		if session.Ready() && !readySent {
			if err := writeJSON(ctx, socket, session.status()); err != nil {
				return err
			}
			if err := writeJSON(ctx, socket, map[string]any{"type": "browser.media.ready", "version": 1}); err != nil {
				return err
			}
			readySent = true
		}
	}
}

func (session *Session) validHello(hello helloMessage, reconnecting bool) bool {
	if !reconnecting {
		return hello.Type == "browser.media.hello" && hello.Ticket != ""
	}
	if hello.Type != "browser.media.resume" {
		return false
	}
	session.mu.Lock()
	expected := session.resumeTicket
	session.mu.Unlock()
	return secureEqual(hello.ResumeTicket, expected)
}

func (session *Session) currentStream() Stream {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.stream
}

func (session *Session) recordEvidence(evidence evidenceMessage) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if evidence.Challenge != session.challenge || evidence.Capture < session.capture ||
		evidence.Playback < session.playback || evidence.Played < session.played {
		return errors.New("browser media evidence is stale or moved backwards")
	}
	session.capture, session.playback, session.played = evidence.Capture, evidence.Playback, evidence.Played
	session.ready = session.inputFrames >= 2 && session.outputFrames >= 2 &&
		session.inputSignal >= 2 && session.outputSignal >= 2 &&
		session.capture >= 2 && session.playback >= 2 && session.played >= 2
	return nil
}

func (session *Session) status() map[string]any {
	session.mu.Lock()
	defer session.mu.Unlock()
	return map[string]any{
		"type": "browser.media.status", "version": 1, "ready": session.ready,
		"evidence": map[string]uint64{
			"browser_to_provider_frames":        session.inputFrames,
			"provider_to_browser_frames":        session.outputFrames,
			"browser_to_provider_signal_frames": session.inputSignal,
			"provider_to_browser_signal_frames": session.outputSignal,
			"capture_callbacks":                 session.capture, "playback_callbacks": session.playback,
			"played_frames": session.played,
		},
	}
}

func hasSignal(frame []byte) bool {
	const minimumSamples = 8
	count := 0
	for index := 0; index+1 < len(frame); index += 2 {
		value := int16(binary.LittleEndian.Uint16(frame[index:]))
		if value > 128 || value < -128 {
			count++
			if count >= minimumSamples {
				return true
			}
		}
	}
	return false
}

type helloMessage struct {
	Type            string `json:"type"`
	Version         int    `json:"version"`
	SessionID       string `json:"session_id"`
	Ticket          string `json:"ticket"`
	ResumeTicket    string `json:"resume_ticket"`
	ConnectionEpoch uint64 `json:"connection_epoch"`
}

type evidenceMessage struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	Challenge string `json:"challenge"`
	Capture   uint64 `json:"capture_callbacks"`
	Playback  uint64 `json:"playback_callbacks"`
	Played    uint64 `json:"played_frames"`
}

func decodeStrict(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("browser media JSON has trailing data")
	}
	return nil
}

func writeJSON(ctx context.Context, socket *websocket.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return socket.Write(ctx, websocket.MessageText, payload)
}

func newChallenge() (string, error) {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func newResumeTicket() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func secureEqual(presented, expected string) bool {
	if presented == "" || expected == "" {
		return false
	}
	want := sha256.Sum256([]byte(expected))
	got := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

func literalLoopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

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
