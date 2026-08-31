// Package core exposes typed state projections without granting the HTTP
// presentation layer any state-machine or recovery authority.
package core

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

const (
	browserSchemaVersion = 1
	defaultBrowserEvery  = 10 * time.Second
	browserWriteTimeout  = 5 * time.Second
	browserAuthClose     = websocket.StatusCode(4401)
)

type Server struct {
	replay        *events.Replay
	now           func() time.Time
	mux           *http.ServeMux
	auth          func(http.Handler) http.Handler
	agents        AgentFacts
	browser       BrowserSessionVerifier
	control       http.Handler
	messages      *providermessages.Store
	messageAPI    http.Handler
	catalog       *linecatalog.Store
	catalogAPI    http.Handler
	lineBootstrap http.Handler
	providerApply http.Handler
	egressProbe   http.Handler
	egressConfig  http.Handler
	egressApply   http.Handler
	cellularSMS   http.Handler
	callHistory   http.Handler
	cellularData  http.Handler
	rawModem      http.Handler
	euiccProfiles http.Handler
	browserEvery  time.Duration
	runtimeInfo   *RuntimeInfo
	providers     ProviderFacts
}

type AgentFacts interface {
	Statuses() []agentlink.ConnectionStatus
	Status(string) (agentlink.ConnectionStatus, bool)
}

type BrowserSessionVerifier interface {
	VerifyBrowserSession(context.Context, *http.Request) (string, error)
}

type ProviderFacts interface {
	CurrentGeneration(string) (string, bool)
}

type BrowserSnapshot struct {
	Type          string                       `json:"type"`
	SchemaVersion int                          `json:"schema_version"`
	Sequence      uint64                       `json:"sequence"`
	At            time.Time                    `json:"at"`
	Lines         []events.LineProjection      `json:"lines"`
	Agents        []agentlink.ConnectionStatus `json:"agents"`
	Messages      []providermessages.Record    `json:"messages,omitempty"`
	Catalog       linecatalog.Snapshot         `json:"catalog"`
	Devices       []DeviceProjection           `json:"devices,omitempty"`
}

type Option func(*Server)

// WithWebUI mounts a presentation-only handler on Core's public listener.
// The UI receives no state-machine authority and must use the same typed,
// authenticated API and WebSocket contracts as any other browser client.
func WithWebUI(handler http.Handler) Option {
	return func(server *Server) {
		if handler != nil {
			server.mux.Handle("GET /{$}", handler)
			server.mux.Handle("GET /index.html", handler)
			server.mux.Handle("GET /assets/app.js", handler)
			server.mux.Handle("GET /assets/call-audio.js", handler)
			server.mux.Handle("GET /assets/call-worklet.js", handler)
			server.mux.Handle("GET /assets/app.css", handler)
			server.mux.Handle("GET /assets/qr/decode.js", handler)
			server.mux.Handle("GET /assets/qr/index.js", handler)
			server.mux.Handle("GET /assets/qr/LICENSE", handler)
		}
	}
}

// WithBrowserMedia mounts the transparent media relay on the same public
// listener as Core's HTTP API. Authentication remains owned by the handler.
func WithBrowserMedia(handler http.Handler) Option {
	return func(server *Server) {
		if handler != nil {
			server.mux.Handle("GET /api/browser-media/{sessionID}/ws", handler)
		}
	}
}

// WithAdminAuth mounts the existing administrator API contract on Core's
// public listener. The handler owns credentials, cookies, sessions and CSRF.
func WithAdminAuth(handler http.Handler) Option {
	return func(server *Server) {
		if handler != nil {
			server.mux.Handle("/api/auth/", handler)
		}
	}
}

func WithAgentLink(handler http.Handler) Option {
	return func(server *Server) {
		if handler != nil {
			server.mux.Handle("GET /v1/agent/ws", handler)
		}
	}
}

// WithAgentMedia mounts the Agent-originated PCM connection beside the Agent
// control route on the same public HTTPS/WSS listener.
func WithAgentMedia(handler http.Handler) Option {
	return func(server *Server) {
		if handler != nil {
			server.mux.Handle("GET /v1/agent/media/ws", handler)
		}
	}
}

func WithAgentData(handler http.Handler) Option {
	return func(server *Server) {
		if handler != nil {
			server.mux.Handle("GET /v1/agent/data/ws", handler)
		}
	}
}

// WithAgentUSBIP mounts the one-time raw USB/IP byte-stream broker on the
// same public TLS listener. No device is exposed merely by mounting it: an
// exact, short-lived Core reservation is still required for every stream.
func WithAgentUSBIP(handler http.Handler) Option {
	return func(server *Server) {
		if handler != nil {
			server.mux.Handle("GET /v1/agent/usbip/ws", handler)
		}
	}
}

// WithCellularMedia mounts browser cellular-media preparation, duplex WSS,
// and exact call control on the same public listener. The handler owns its
// cookie/CSRF capabilities and 10-second call lease boundary.
func WithCellularMedia(handler http.Handler) Option {
	return func(server *Server) {
		if handler != nil {
			server.mux.Handle("/v1/cellular/media/leases", handler)
			server.mux.Handle("GET /api/cellular-browser-media/{sessionID}/ws", handler)
			server.mux.Handle("GET /v1/lines/{lineID}/cellular/calls/{operation}", handler)
			server.mux.Handle("POST /v1/lines/{lineID}/cellular/calls/{operation}", handler)
		}
	}
}

func WithAgentFacts(facts AgentFacts) Option {
	return func(server *Server) { server.agents = facts }
}

func WithProviderFacts(facts ProviderFacts) Option {
	return func(server *Server) { server.providers = facts }
}

// WithEgressProbe mounts explicit, read-only country-exit status and active
// end-to-end UDP diagnostics. The handler cannot apply routes or recover a
// provider and therefore remains separate from Provider apply authority.
func WithEgressProbe(handler http.Handler) Option {
	return func(server *Server) { server.egressProbe = handler }
}

// WithEgressConfig mounts the durable desired country-exit library separately
// from the explicit privileged apply boundary. Saving never changes live routes.
func WithEgressConfig(configHandler, applyHandler http.Handler) Option {
	return func(server *Server) {
		server.egressConfig = configHandler
		server.egressApply = applyHandler
	}
}

func WithRuntimeInfo(info RuntimeInfo) Option {
	return func(server *Server) {
		copy := info
		server.runtimeInfo = &copy
	}
}

// WithBrowserControl exposes one authenticated, same-origin state stream on
// Core's existing public listener. Browser mutations remain on the existing
// CSRF-protected HTTP contract until their idempotency semantics are migrated.
func WithBrowserControl(verifier BrowserSessionVerifier) Option {
	return func(server *Server) { server.browser = verifier }
}

// WithVoWiFiControl mounts authenticated, CSRF-protected HTTP mutations on
// the same public listener as Agent, browser-state and media WebSockets.
func WithVoWiFiControl(handler http.Handler) Option {
	return func(server *Server) { server.control = handler }
}

// WithMessages mounts the authenticated message history on Core's existing
// public listener and includes a bounded recent window in the existing browser
// state stream. It does not create a second browser socket.
func WithMessages(store *providermessages.Store, handler http.Handler) Option {
	return func(server *Server) {
		server.messages = store
		server.messageAPI = handler
	}
}

// WithCellularMessages mounts typed Agent-backed SMS list/send operations on
// the existing public listener. Core's management middleware supplies the
// same cookie/CSRF contract used by VoWiFi message submission.
func WithCellularMessages(handler http.Handler) Option {
	return func(server *Server) { server.cellularSMS = handler }
}

func WithCallHistory(handler http.Handler) Option {
	return func(server *Server) { server.callHistory = handler }
}

// WithCellularData mounts explicit, quota-limited borrowing sessions. The
// handler owns its ephemeral SOCKS listener and Agent flow capabilities.
func WithCellularData(handler http.Handler) Option {
	return func(server *Server) { server.cellularData = handler }
}

// WithRawModem mounts the explicit adapted/raw whole-modem binding. The
// handler re-resolves a live exact Agent+equipment+ICCID candidate before it
// writes intent; the presentation layer cannot submit transport identities.
func WithRawModem(handler http.Handler) Option {
	return func(server *Server) { server.rawModem = handler }
}

// WithEUICCProfiles mounts current multi-reader inventory and reversible
// profile state changes. The management middleware supplies auth and CSRF;
// the handler never owns a PC/SC reader or caches a hardware state machine.
func WithEUICCProfiles(handler http.Handler) Option {
	return func(server *Server) { server.euiccProfiles = handler }
}

// WithLineCatalog mounts the read-only desired-line catalog. Runtime mutations
// remain separate; catalog entries never carry observed health or generations.
func WithLineCatalog(store *linecatalog.Store, handler http.Handler) Option {
	return func(server *Server) {
		server.catalog = store
		server.catalogAPI = handler
	}
}

// WithLineBootstrap mounts the live-card candidate projection and explicit
// disabled-draft creation endpoint. The handler has no runtime or Provider
// authority and is protected by the normal management auth/CSRF middleware.
func WithLineBootstrap(handler http.Handler) Option {
	return func(server *Server) { server.lineBootstrap = handler }
}

// WithProviderApply mounts the explicit administrator-triggered provider
// configuration transaction. The handler is a typed proxy to the local
// privileged helper; Core itself remains unprivileged.
func WithProviderApply(handler http.Handler) Option {
	return func(server *Server) { server.providerApply = handler }
}

// WithMediaLeases mounts the authenticated browser HTTP endpoint that creates
// and revokes opaque capabilities consumed by the media WebSocket route.
func WithMediaLeases(handler http.Handler) Option {
	return func(server *Server) {
		if handler != nil {
			server.mux.Handle("/v1/media/leases", handler)
		}
	}
}

// WithManagementAuth protects Core's management facts API while leaving
// health, Agent and media endpoints to their own narrower authentication.
func WithManagementAuth(middleware func(http.Handler) http.Handler) Option {
	return func(server *Server) { server.auth = middleware }
}

func NewServer(replay *events.Replay, now func() time.Time, options ...Option) *Server {
	if now == nil {
		now = time.Now
	}
	server := &Server{replay: replay, now: now, mux: http.NewServeMux(), browserEvery: defaultBrowserEvery}
	for _, option := range options {
		if option != nil {
			option(server)
		}
	}
	server.mux.HandleFunc("GET /healthz", server.health)
	server.mux.Handle("GET /v1/lines", server.protect(http.HandlerFunc(server.lines)))
	server.mux.Handle("GET /v1/lines/{lineID}", server.protect(http.HandlerFunc(server.line)))
	server.mux.Handle("GET /v1/agents", server.protect(http.HandlerFunc(server.agentList)))
	server.mux.Handle("GET /v1/agents/{agentID}", server.protect(http.HandlerFunc(server.agent)))
	server.mux.Handle("GET /v1/devices", server.protect(http.HandlerFunc(server.devices)))
	server.mux.Handle("GET /v1/system/runtime", server.protect(http.HandlerFunc(server.runtime)))
	server.mux.Handle("GET /v1/diagnostics", server.protect(http.HandlerFunc(server.diagnostics)))
	if server.control != nil {
		server.mux.Handle("GET /v1/lines/{lineID}/vowifi/{operation...}", server.protect(server.control))
		server.mux.Handle("POST /v1/lines/{lineID}/vowifi/{operation...}", server.protect(server.control))
	}
	if server.messageAPI != nil {
		server.mux.Handle("GET /v1/messages", server.protect(server.messageAPI))
	}
	if server.callHistory != nil {
		server.mux.Handle("GET /v1/calls", server.protect(server.callHistory))
		server.mux.Handle("DELETE /v1/calls", server.protect(server.callHistory))
	}
	if server.cellularSMS != nil {
		server.mux.Handle("GET /v1/lines/{lineID}/cellular/messages", server.protect(server.cellularSMS))
		server.mux.Handle("POST /v1/lines/{lineID}/cellular/messages", server.protect(server.cellularSMS))
	}
	if server.cellularData != nil {
		server.mux.Handle("GET /v1/lines/{lineID}/cellular/data/sessions", server.protect(server.cellularData))
		server.mux.Handle("POST /v1/lines/{lineID}/cellular/data/sessions", server.protect(server.cellularData))
		server.mux.Handle("DELETE /v1/lines/{lineID}/cellular/data/sessions/{sessionID}", server.protect(server.cellularData))
	}
	if server.rawModem != nil {
		server.mux.Handle("GET /v1/lines/{lineID}/raw-modem", server.protect(server.rawModem))
		server.mux.Handle("PUT /v1/lines/{lineID}/raw-modem", server.protect(server.rawModem))
	}
	if server.euiccProfiles != nil {
		server.mux.Handle("GET /v1/euiccs", server.protect(server.euiccProfiles))
		server.mux.Handle("POST /v1/euiccs/{eid}/profiles/{iccid}/{action}", server.protect(server.euiccProfiles))
		server.mux.Handle("POST /v1/euiccs/{eid}/downloads", server.protect(server.euiccProfiles))
		server.mux.Handle("GET /v1/euiccs/{eid}/downloads/{operation_id}", server.protect(server.euiccProfiles))
		server.mux.Handle("POST /v1/euiccs/{eid}/downloads/{operation_id}/cancel", server.protect(server.euiccProfiles))
		server.mux.Handle("POST /v1/euiccs/{eid}/discovery", server.protect(server.euiccProfiles))
		server.mux.Handle("GET /v1/euiccs/{eid}/notifications", server.protect(server.euiccProfiles))
		server.mux.Handle("POST /v1/euiccs/{eid}/notifications/{sequence}/deliver", server.protect(server.euiccProfiles))
		server.mux.Handle("POST /v1/euiccs/{eid}/notifications/{sequence}/remove", server.protect(server.euiccProfiles))
	}
	if server.catalogAPI != nil {
		server.mux.Handle("GET /v1/catalog/lines", server.protect(server.catalogAPI))
		server.mux.Handle("GET /v1/catalog/lines/{lineID}", server.protect(server.catalogAPI))
		server.mux.Handle("PUT /v1/catalog/lines/{lineID}", server.protect(server.catalogAPI))
	}
	if server.lineBootstrap != nil {
		server.mux.Handle("GET /v1/line-candidates", server.protect(server.lineBootstrap))
		server.mux.Handle("POST /v1/line-candidates/{candidateID}/claim", server.protect(server.lineBootstrap))
	}
	if server.providerApply != nil {
		server.mux.Handle("GET /v1/system/provider-config", server.protect(server.providerApply))
		server.mux.Handle("POST /v1/system/provider-config", server.protect(server.providerApply))
	}
	if server.egressProbe != nil {
		server.mux.Handle("GET /v1/egress/exits", server.protect(server.egressProbe))
		server.mux.Handle("POST /v1/egress/exits/{country}/test", server.protect(server.egressProbe))
	}
	if server.egressConfig != nil {
		server.mux.Handle("GET /v1/egress/config", server.protect(server.egressConfig))
		server.mux.Handle("PUT /v1/egress/config", server.protect(server.egressConfig))
	}
	if server.egressApply != nil {
		server.mux.Handle("GET /v1/egress/config/apply", server.protect(server.egressApply))
		server.mux.Handle("POST /v1/egress/config/apply", server.protect(server.egressApply))
	}
	if server.browser != nil {
		server.mux.HandleFunc("GET /ws", server.browserState)
		server.mux.HandleFunc("GET /v1/browser/ws", server.browserState)
	}
	return server
}

func (s *Server) browserState(response http.ResponseWriter, request *http.Request) {
	subject, err := s.browser.VerifyBrowserSession(request.Context(), request)
	if err != nil || strings.TrimSpace(subject) == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"code": "authentication_required"})
		return
	}
	socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer socket.CloseNow()
	readContext := socket.CloseRead(request.Context())
	ticker := time.NewTicker(s.browserEvery)
	defer ticker.Stop()
	for sequence := uint64(1); ; sequence++ {
		if sequence > 1 {
			current, verifyErr := s.browser.VerifyBrowserSession(request.Context(), request)
			if verifyErr != nil || current != subject {
				_ = socket.Close(browserAuthClose, "browser session expired")
				return
			}
		}
		if err := s.writeBrowserSnapshot(request.Context(), socket, sequence); err != nil {
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-readContext.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) writeBrowserSnapshot(parent context.Context, socket *websocket.Conn, sequence uint64) error {
	ctx, cancel := context.WithTimeout(parent, browserWriteTimeout)
	defer cancel()
	agents := []agentlink.ConnectionStatus{}
	if s.agents != nil {
		agents = s.agents.Statuses()
	}
	at := s.now().UTC()
	messages := []providermessages.Record{}
	if s.messages != nil {
		var err error
		messages, err = s.messages.List("", 50)
		if err != nil {
			return err
		}
	}
	lines := s.replay.Projections(at)
	catalog := linecatalog.Snapshot{SchemaVersion: linecatalog.SchemaVersion, Lines: []linecatalog.Line{}}
	var devices []DeviceProjection
	if s.catalog != nil {
		var err error
		catalog, err = s.catalog.Snapshot()
		if err != nil {
			return err
		}
		// Devices is an additive, fail-closed presentation. A raw-binding read
		// failure must not take the existing lines/agents/messages state stream
		// offline; omitting devices also prevents the browser from selecting any
		// cellular endpoint until the dedicated projection is readable again.
		if rawBindings, rawErr := s.catalog.RawModemBindings(); rawErr == nil {
			devices = projectDevices(at, agents, catalog, lines, rawBindings).Devices
		}
	} else {
		devices = projectDevices(at, agents, catalog, lines, linecatalog.RawModemSnapshot{
			SchemaVersion: linecatalog.RawModemBindingSchemaVersion, Bindings: []linecatalog.RawModemBinding{},
		}).Devices
	}
	return wsjson.Write(ctx, socket, BrowserSnapshot{
		Type: "browser.snapshot", SchemaVersion: browserSchemaVersion, Sequence: sequence,
		At: at, Lines: lines, Agents: agents, Messages: messages, Catalog: catalog, Devices: devices,
	})
}

func (s *Server) agentList(response http.ResponseWriter, _ *http.Request) {
	agents := []agentlink.ConnectionStatus{}
	if s.agents != nil {
		agents = s.agents.Statuses()
	}
	writeJSON(response, http.StatusOK, map[string]any{"at": s.now().UTC(), "agents": agents})
}

func (s *Server) agent(response http.ResponseWriter, request *http.Request) {
	if s.agents != nil {
		if status, found := s.agents.Status(strings.TrimSpace(request.PathValue("agentID"))); found {
			writeJSON(response, http.StatusOK, status)
			return
		}
	}
	writeJSON(response, http.StatusNotFound, map[string]string{"code": "agent_offline"})
}

func (s *Server) protect(handler http.Handler) http.Handler {
	if s.auth == nil {
		return handler
	}
	return s.auth(handler)
}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	s.mux.ServeHTTP(response, request)
}

func (s *Server) health(response http.ResponseWriter, _ *http.Request) {
	lines := s.replay.Projections(s.now().UTC())
	writeJSON(response, http.StatusOK, map[string]any{
		"status": "ok", "mode": "read_only_replay", "lines": len(lines),
		"last_received_at": s.replay.LastReceivedAt(),
	})
}

func (s *Server) lines(response http.ResponseWriter, _ *http.Request) {
	at := s.now().UTC()
	writeJSON(response, http.StatusOK, map[string]any{
		"at": at, "lines": s.replay.Projections(at),
	})
}

func (s *Server) line(response http.ResponseWriter, request *http.Request) {
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	at := s.now().UTC()
	for _, projection := range s.replay.Projections(at) {
		if projection.LineID == lineID {
			writeJSON(response, http.StatusOK, projection)
			return
		}
	}
	writeJSON(response, http.StatusNotFound, map[string]string{"code": "line_not_found"})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func ValidateListenAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
