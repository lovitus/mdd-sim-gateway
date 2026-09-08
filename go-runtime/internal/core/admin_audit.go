package core

import (
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systempreferences"
)

type adminAudit struct {
	preferences interface {
		Snapshot() (systempreferences.Snapshot, error)
	}
	store interface {
		AppendAdminAudit(events.AdminAuditRecord) error
		AdminAudit(int) ([]events.AdminAuditRecord, error)
	}
	lastWarning atomic.Int64
}

func WithAdminAudit(preferences *systempreferences.Store, store *events.BoltStore) Option {
	return func(server *Server) {
		if preferences != nil && store != nil {
			server.audit = &adminAudit{preferences: preferences, store: store}
		}
	}
}

type auditResponse struct {
	http.ResponseWriter
	status int
}

func (response *auditResponse) Unwrap() http.ResponseWriter { return response.ResponseWriter }
func (response *auditResponse) WriteHeader(status int) {
	if response.status == 0 && status >= 200 {
		response.status = status
	}
	response.ResponseWriter.WriteHeader(status)
}
func (response *auditResponse) Write(value []byte) (int, error) {
	if response.status == 0 {
		response.WriteHeader(http.StatusOK)
	}
	return response.ResponseWriter.Write(value)
}

// Ported from ec620942 main.py:_audit_client. Forwarded addresses affect only
// audit presentation, never authentication, rate limits or call authority.
func auditClient(request *http.Request, trusted []string) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	for _, network := range trusted {
		prefix, err := netip.ParsePrefix(network)
		if err != nil || (!prefix.Contains(peer) && !prefix.Contains(peer.Unmap())) {
			continue
		}
		first, _, _ := strings.Cut(request.Header.Get("X-Forwarded-For"), ",")
		if forwarded, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil {
			return forwarded.Unmap().String()
		}
		break
	}
	return peer.Unmap().String()
}

func (audit *adminAudit) record(request *http.Request, status int, at time.Time) {
	preferences, err := audit.preferences.Snapshot()
	if err == nil && preferences.Preferences.AuditEnabled != nil && !*preferences.Preferences.AuditEnabled {
		return
	}
	if err == nil {
		route := strings.TrimPrefix(request.Pattern, request.Method+" ")
		if route == "/api/auth/" {
			switch request.URL.Path {
			case "/api/auth/login", "/api/auth/logout", "/api/auth/password", "/api/auth/setup", "/api/auth/agent-credentials":
				route = request.URL.Path
			}
		}
		if route == "" {
			route = "unmatched"
		}
		err = audit.store.AppendAdminAudit(events.AdminAuditRecord{At: at.UTC(), Method: request.Method, Route: route, Status: status,
			Client: auditClient(request, preferences.Preferences.TrustedProxies)})
	}
	if err != nil {
		now := time.Now().Unix()
		prior := audit.lastWarning.Load()
		if now-prior >= 30 && audit.lastWarning.CompareAndSwap(prior, now) {
			log.Printf("administrative audit unavailable")
		}
	}
}

func (server *Server) auditHistory(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.RawQuery != "" {
		writeJSON(response, 400, map[string]string{"code": "invalid_audit_request"})
		return
	}
	entries, err := server.audit.store.AdminAudit(200)
	if err != nil {
		writeJSON(response, 503, map[string]string{"code": "audit_unavailable"})
		return
	}
	writeJSON(response, 200, map[string]any{"entries": entries})
}
