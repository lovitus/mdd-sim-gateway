package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/updatenetwork"
)

func TestUpdaterProxySnapshotDoesNotRedirectLocalToken(t *testing.T) {
	var leaked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case updatenetwork.PolicyPath:
			_ = json.NewEncoder(w).Encode(updatenetwork.PolicyRevision{SchemaVersion: 1, Revision: 1})
		case egressconfig.SnapshotIPCPath:
			http.Redirect(w, r, "/capture", http.StatusFound)
		case "/capture":
			leaked.Add(1)
		}
	}))
	defer server.Close()
	core := coreMaintenance{baseURL: server.URL, token: strings.Repeat("t", 32)}
	_, err := core.updateClient(t.Context(), &updatenetwork.Route{Mode: "library", ProfileID: "proxy-a", ConfigRevision: 1, PolicyRevision: 1})
	if err == nil || leaked.Load() != 0 {
		t.Fatal("proxy snapshot redirected privileged token", err, leaked.Load())
	}
}

func TestUpdaterRejectsPolicyChangeWhileResolvingProxy(t *testing.T) {
	var revision atomic.Uint64
	revision.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case updatenetwork.PolicyPath:
			_ = json.NewEncoder(w).Encode(updatenetwork.PolicyRevision{SchemaVersion: 1, Revision: revision.Load()})
		case egressconfig.SnapshotIPCPath:
			revision.Store(2)
			_ = json.NewEncoder(w).Encode(egressconfig.Snapshot{SchemaVersion: egressconfig.SchemaVersion, Revision: 1,
				Config: egressconfig.Config{SchemaVersion: egressconfig.SchemaVersion, Profiles: map[string]egressconfig.Profile{
					"proxy-a": {Name: "fixture", Type: "socks5", Server: "192.0.2.1", Port: 1080},
				}, Exits: map[string]egressconfig.Exit{}}})
		}
	}))
	defer server.Close()
	core := coreMaintenance{baseURL: server.URL, token: strings.Repeat("t", 32)}
	client, err := core.updateClient(t.Context(), &updatenetwork.Route{Mode: "library", ProfileID: "proxy-a", ConfigRevision: 1, PolicyRevision: 1})
	if err == nil || client != nil || err.Error() != "update networking settings changed" {
		t.Fatal("changed policy produced an obsolete download client")
	}
}
