package cellularmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const browserMessageLimit = 4096
const browserWriteTimeout = 5 * time.Second

type browserConnection struct {
	socket *websocket.Conn
	writes sync.Mutex
	mu     sync.Mutex
	ready  bool
}

type browserHello struct {
	Type            string `json:"type"`
	Version         int    `json:"version"`
	SessionID       string `json:"session_id"`
	Ticket          string `json:"ticket,omitempty"`
	ResumeTicket    string `json:"resume_ticket,omitempty"`
	ConnectionEpoch uint64 `json:"connection_epoch,omitempty"`
}

type browserEvidence struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	Challenge string `json:"challenge"`
	Capture   uint64 `json:"capture_callbacks"`
	Playback  uint64 `json:"playback_callbacks"`
	Played    uint64 `json:"played_frames"`
}

func (service *Service) serveBrowser(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.URL.RawQuery != "" || !sameOrigin(request) {
		http.Error(response, "same_origin_required", http.StatusForbidden)
		return
	}
	subject, err := service.config.Auth.VerifyBrowserSession(request.Context(), request)
	if err != nil || strings.TrimSpace(subject) == "" {
		http.Error(response, "login_required", http.StatusUnauthorized)
		return
	}
	sessionID := strings.TrimSpace(request.PathValue("sessionID"))
	if sessionID == "" {
		trimmed := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/api/cellular-browser-media/"), "/ws")
		sessionID = strings.Trim(trimmed, "/")
	}
	current := service.lookup(sessionID)
	if current == nil || current.subject != subject {
		http.Error(response, "cellular_media_not_found", http.StatusNotFound)
		return
	}
	current.mu.Lock()
	expired := current.phase == "expired" || current.phase == "ended" || current.terminal
	current.mu.Unlock()
	if expired {
		http.Error(response, "cellular_media_expired", http.StatusGone)
		return
	}
	socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	socket.SetReadLimit(browserMessageLimit)
	connection := &browserConnection{socket: socket}
	defer socket.CloseNow()
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	helloContext, cancelHello := context.WithTimeout(ctx, 10*time.Second)
	messageType, payload, err := socket.Read(helloContext)
	cancelHello()
	if err != nil || messageType != websocket.MessageText {
		_ = socket.Close(websocket.StatusPolicyViolation, "browser media hello required")
		return
	}
	var hello browserHello
	if decodeStrict(payload, &hello) != nil || hello.Version != 1 || hello.SessionID != current.id {
		_ = socket.Close(websocket.StatusPolicyViolation, "invalid browser media hello")
		return
	}
	claimed, err := service.claimBrowser(current, connection, hello)
	if err != nil {
		_ = socket.Close(websocket.StatusPolicyViolation, "browser media session conflict")
		return
	}
	defer service.releaseBrowser(current, connection)
	if old := claimed.old; old != nil {
		old.close(websocket.StatusGoingAway, "cellular media resumed elsewhere")
	}
	current.peer.Drain()
	messageName := "browser.media.claimed"
	if claimed.resumed {
		messageName = "browser.media.resumed"
	}
	if err := connection.writeJSON(ctx, map[string]any{
		"type": messageName, "version": 1, "challenge": claimed.challenge,
		"resume_ticket": claimed.resumeTicket, "connection_epoch": claimed.epoch,
	}); err != nil {
		return
	}
	if err := connection.writeJSON(ctx, map[string]any{
		"type": "browser.media.started", "version": 1, "purpose": claimed.purpose,
	}); err != nil {
		return
	}
	pumpDone := make(chan error, 1)
	go func() { pumpDone <- service.pumpDownlink(ctx, current, connection) }()
	readErr := service.readBrowser(ctx, current, connection)
	cancel()
	select {
	case <-pumpDone:
	case <-time.After(time.Second):
	}
	_ = readErr
}

type browserClaim struct {
	old          *browserConnection
	resumed      bool
	challenge    string
	resumeTicket string
	epoch        uint64
	purpose      string
}

func (service *Service) claimBrowser(current *session, connection *browserConnection, hello browserHello) (browserClaim, error) {
	challenge, err := randomID()
	if err != nil {
		return browserClaim{}, err
	}
	resumeTicket, err := randomID()
	if err != nil {
		return browserClaim{}, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	resumed := hello.Type == "browser.media.resume"
	if !resumed {
		if hello.Type != "browser.media.hello" || current.connectionEpoch != 0 || !secureEqual(hello.Ticket, current.callID) {
			return browserClaim{}, errors.New("invalid initial browser media claim")
		}
	} else if !secureEqual(hello.ResumeTicket, current.resumeTicket) || hello.ConnectionEpoch != current.connectionEpoch {
		return browserClaim{}, errors.New("invalid browser media resume identity")
	}
	old := current.connection
	current.connectionID++
	current.connectionEpoch++
	current.connection = connection
	current.challenge = challenge
	current.resumeTicket = resumeTicket
	current.lastHeartbeat = service.config.Now().UTC()
	purpose := "canary"
	if current.phase == "active" || current.phase == "uncertain" || current.phase == "ending" || current.phase == "hangup_unconfirmed" {
		purpose = "call"
	}
	return browserClaim{
		old: old, resumed: resumed, challenge: challenge, resumeTicket: resumeTicket,
		epoch: current.connectionEpoch, purpose: purpose,
	}, nil
}

func (service *Service) releaseBrowser(current *session, connection *browserConnection) {
	current.mu.Lock()
	if current.connection == connection {
		current.connection = nil
	}
	current.mu.Unlock()
}

func (service *Service) readBrowser(ctx context.Context, current *session, connection *browserConnection) error {
	for {
		messageType, payload, err := connection.socket.Read(ctx)
		if err != nil {
			return err
		}
		now := service.config.Now().UTC()
		switch messageType {
		case websocket.MessageBinary:
			if len(payload) != pcmFrameBytes {
				return errors.New("browser sent an invalid PCM frame")
			}
			if err := current.peer.Write(ctx, payload); err != nil {
				return err
			}
			current.mu.Lock()
			current.lastHeartbeat = now
			current.inputFrames++
			if hasSignal(payload) {
				current.inputSignal++
			}
			canary := current.phase == "ready"
			current.mu.Unlock()
			if canary {
				if err := connection.writeBinary(ctx, payload); err != nil {
					return err
				}
				current.mu.Lock()
				current.outputFrames++
				if hasSignal(payload) {
					current.outputSignal++
				}
				current.mu.Unlock()
			}
		case websocket.MessageText:
			var evidence browserEvidence
			if decodeStrict(payload, &evidence) != nil || evidence.Type != "browser.media.evidence" || evidence.Version != 1 {
				return errors.New("invalid browser media evidence")
			}
			current.mu.Lock()
			if evidence.Challenge != current.challenge || evidence.Capture < current.capture ||
				evidence.Playback < current.playback || evidence.Played < current.played {
				current.mu.Unlock()
				return errors.New("stale browser media evidence")
			}
			current.capture, current.playback, current.played = evidence.Capture, evidence.Playback, evidence.Played
			current.lastHeartbeat = now
			current.mu.Unlock()
		default:
			return errors.New("invalid browser media message type")
		}
		if err := service.maybeReady(ctx, current, connection); err != nil {
			return err
		}
	}
}

func (service *Service) pumpDownlink(ctx context.Context, current *session, connection *browserConnection) error {
	for {
		frame, err := current.peer.Read(ctx)
		if err != nil {
			return err
		}
		if err := connection.writeBinary(ctx, frame); err != nil {
			return err
		}
		current.mu.Lock()
		current.outputFrames++
		if hasSignal(frame) {
			current.outputSignal++
		}
		current.mu.Unlock()
		if err := service.maybeReady(ctx, current, connection); err != nil {
			return err
		}
	}
}

func (service *Service) maybeReady(ctx context.Context, current *session, connection *browserConnection) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.ready {
		return nil
	}
	current.mu.Lock()
	ready := current.canaryReady || current.inputFrames >= 5 && current.inputSignal >= 2 &&
		current.outputFrames >= 2 && current.outputSignal >= 2 && current.capture >= 2 &&
		current.playback >= 2 && current.played >= 2
	if ready {
		current.canaryReady = true
	}
	status := map[string]any{
		"type": "browser.media.status", "version": 1, "ready": ready,
		"evidence": map[string]uint64{
			"browser_to_agent_frames": current.inputFrames, "agent_to_browser_frames": current.outputFrames,
			"browser_signal_frames": current.inputSignal, "browser_played_signal_frames": current.outputSignal,
			"capture_callbacks": current.capture, "playback_callbacks": current.playback, "played_frames": current.played,
		},
	}
	current.mu.Unlock()
	if !ready {
		return nil
	}
	if err := connection.writeJSON(ctx, status); err != nil {
		return err
	}
	if err := connection.writeJSON(ctx, map[string]any{"type": "browser.media.ready", "version": 1}); err != nil {
		return err
	}
	connection.ready = true
	return nil
}

func (connection *browserConnection) writeJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	connection.writes.Lock()
	defer connection.writes.Unlock()
	writeContext, cancel := context.WithTimeout(ctx, browserWriteTimeout)
	defer cancel()
	return connection.socket.Write(writeContext, websocket.MessageText, payload)
}

func (connection *browserConnection) writeBinary(ctx context.Context, payload []byte) error {
	connection.writes.Lock()
	defer connection.writes.Unlock()
	writeContext, cancel := context.WithTimeout(ctx, browserWriteTimeout)
	defer cancel()
	return connection.socket.Write(writeContext, websocket.MessageBinary, payload)
}

func (connection *browserConnection) close(code websocket.StatusCode, reason string) {
	connection.writes.Lock()
	defer connection.writes.Unlock()
	_ = connection.socket.Close(code, reason)
}

func decodeStrict(payload []byte, target any) error {
	if len(payload) == 0 || len(payload) > browserMessageLimit {
		return errors.New("invalid browser media message size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("browser media message has trailing JSON")
	}
	return nil
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, request.Host)
}

func secureEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func hasSignal(frame []byte) bool {
	count := 0
	for index := 0; index+1 < len(frame); index += 2 {
		value := int16(binary.LittleEndian.Uint16(frame[index:]))
		if value > 128 || value < -128 {
			count++
			if count >= 8 {
				return true
			}
		}
	}
	return false
}

const pcmFrameBytes = 320
