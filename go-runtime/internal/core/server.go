// Package core exposes typed state projections without granting the HTTP
// presentation layer any state-machine or recovery authority.
package core

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
)

type Server struct {
	replay *events.Replay
	now    func() time.Time
	mux    *http.ServeMux
	auth   func(http.Handler) http.Handler
	agents AgentFacts
}

type AgentFacts interface {
	Statuses() []agentlink.ConnectionStatus
	Status(string) (agentlink.ConnectionStatus, bool)
}

type Option func(*Server)

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

func WithAgentFacts(facts AgentFacts) Option {
	return func(server *Server) { server.agents = facts }
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
	server := &Server{replay: replay, now: now, mux: http.NewServeMux()}
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
	return server
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
