package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/euiccprofiles"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemupdate"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

// Only external hardware/Provider I/O is simulated. Both maintenance owners,
// Core's eSIM guard, Agent host leases, persistence and HTTP admission are real.
type updateProfileAgents struct {
	*agentlink.Server
	status               agentlink.ConnectionStatus
	rejectHost           bool
	mutations, refreshes int
}

func (a *updateProfileAgents) Statuses() []agentlink.ConnectionStatus {
	return []agentlink.ConnectionStatus{a.status}
}
func (a *updateProfileAgents) BeginHostMaintenance(agent, generation, lease string) error {
	if a.rejectHost {
		return errors.New("host acquisition unavailable")
	}
	return a.Server.BeginHostMaintenance(agent, generation, lease)
}
func (a *updateProfileAgents) ExecuteEUICCProfile(_ context.Context, _, _ string, r agentlink.EUICCProfileRequest) (agentlink.EUICCProfileResponse, error) {
	result := agentlink.EUICCProfileResponse{OperationID: r.OperationID, SessionGeneration: r.SessionGeneration, EID: r.EID, ICCID: r.ICCID, Action: r.Action}
	if r.Action == agentlink.EUICCProfileRefresh {
		a.refreshes++
		result.Outcome = agentlink.EUICCProfileRefreshed
		result.Inventory = a.status.Topology.Readers[0].EUICC
	} else {
		a.mutations++
		result.Outcome = agentlink.EUICCProfileUncertain
	}
	return result, result.ValidateFor(r)
}
func (a *updateProfileAgents) ExecuteEUICCProfileCommand(ctx context.Context, c agentlink.EUICCProfileCommand) (agentlink.EUICCProfileResponse, error) {
	return a.ExecuteEUICCProfile(ctx, a.status.AgentID, a.status.ProcessGeneration, agentlink.EUICCProfileRequest{
		OperationID: c.OperationID, SessionGeneration: "insert", EID: c.EID, Action: c.Action,
	})
}

type updateProfileProvider struct {
	mu          sync.Mutex
	catalog     *linecatalog.Store
	lease       string
	failRelease bool
	updatePosts int
}

func (p *updateProfileProvider) statusLocked() vowifiipc.Snapshot {
	s := vowifiipc.Snapshot{SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-a", ProviderID: "provider-a", ProcessGeneration: "provider-line-a", Sequence: 1, ObservedAt: time.Now(), Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeStopped}}
	s.Tunnel.Condition, s.IMS.Condition = vowifiipc.LayerStopped, vowifiipc.LayerStopped
	s.Voice.Condition, s.Messaging.Condition = vowifiipc.LayerStopped, vowifiipc.LayerStopped
	if p.lease != "" {
		s.Maintenance = vowifiipc.MaintenanceStatus{Draining: true, Code: "apply_drain", LeaseID: p.lease}
	}
	return s
}
func (p *updateProfileProvider) Status(context.Context, string) (vowifiipc.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.statusLocked(), nil
}
func (p *updateProfileProvider) Snapshot(context.Context) (providerapply.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	catalog, err := p.catalog.Snapshot()
	s := p.statusLocked()
	return providerapply.Snapshot{SchemaVersion: 1, CatalogRevision: catalog.Revision, Lines: []providerapply.LineStatus{{LineID: s.LineID, ProviderPresent: true, Code: "provider_reachable", ProcessGeneration: s.ProcessGeneration, Maintenance: s.Maintenance}}}, err
}
func (p *updateProfileProvider) changeLocked(lease string, begin bool) error {
	// Match the Provider's real rejection: a foreign lease is never overwritten.
	if p.lease != "" && p.lease != lease {
		return errors.New("maintenance_busy")
	}
	if begin {
		p.lease = lease
	} else if p.failRelease {
		p.failRelease = false
		return errors.New("resume unavailable")
	} else {
		p.lease = ""
	}
	return nil
}
func (p *updateProfileProvider) Request(_ context.Context, r providerapply.DrainRequest, begin bool) (providerapply.DrainResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.changeLocked(r.LeaseID, begin)
	if err != nil {
		return providerapply.DrainResult{}, err
	}
	code := "drained"
	if !begin {
		code = "resumed"
	}
	return providerapply.DrainResult{SchemaVersion: 1, CatalogRevision: r.CatalogRevision, LeaseID: r.LeaseID, Ready: true, Lines: []providerapply.DrainLineResult{{LineID: "line-a", ProcessGeneration: "provider-line-a", Code: code}}}, nil
}
func (p *updateProfileProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 32) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(p.statusLocked())
		return
	}
	var input vowifiipc.MaintenanceRequest
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p.updatePosts++
	if err := p.changeLocked(input.LeaseID, r.URL.Path == "/v1/maintenance/drain"); err != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "maintenance_busy"})
		return
	}
	_ = json.NewEncoder(w).Encode(vowifiipc.MaintenanceResult{LeaseID: input.LeaseID, Draining: p.lease != "", Status: p.statusLocked()})
}

func TestUpdateRejectsRetainedESIMWithoutBlockingOriginalRecovery(t *testing.T) {
	for _, cleanupOnly := range []bool{false, true} {
		name := "unknown"
		if cleanupOnly {
			name = "cleanup-only-before-host"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			const eid = "89049032000000000000000000000001"
			const card = "8944100000000000001"
			const token = "0123456789abcdef0123456789abcdef"
			agentServer, err := agentlink.NewServer(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return token, nil }))
			if err != nil {
				t.Fatal(err)
			}
			host := httptest.NewServer(agentServer)
			defer host.Close()
			socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(host.URL, "http"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}, "X-Mdd-Agent-Id": {"agent"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer socket.CloseNow()
			if err := wsjson.Write(ctx, socket, map[string]any{"kind": "hello", "hello": agentlink.Hello{SchemaVersion: 1, AgentID: "agent", ProcessGeneration: "process"}}); err != nil {
				t.Fatal(err)
			}
			var ack map[string]any
			if err := wsjson.Read(ctx, socket, &ack); err != nil || ack["kind"] != "hello_ack" {
				t.Fatalf("hello: %v %v", ack, err)
			}
			catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer catalog.Close()
			if _, err := catalog.Put(deviceTestLine("line-a", card, "")); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			agents := &updateProfileAgents{Server: agentServer, rejectHost: cleanupOnly, status: agentlink.ConnectionStatus{AgentID: "agent", ProcessGeneration: "process", LastReport: now, LastSeen: now, Topology: &agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{{ReaderName: "reader", SessionGeneration: "insert", CardPresent: true, CardID: card, IdentityState: agentlink.CardIdentified, EUICC: &agentlink.EUICCFact{EID: eid, InventoryRefresh: true, ProfilesAvailable: true, ProfileManagement: true, Profiles: []agentlink.EUICCProfileFact{{ICCID: card, State: agentlink.EUICCProfileDisabled}}}}}}}}
			provider := &updateProfileProvider{catalog: catalog, failRelease: cleanupOnly}
			endpoint := httptest.NewServer(provider)
			defer endpoint.Close()
			directory := mediaauth.NewProviderDirectory()
			if err := directory.Replace(mediaauth.Provider{LineID: "line-a", ProviderID: "provider-a", Generation: "provider-line-a", CardID: card, BaseURL: "ws" + strings.TrimPrefix(endpoint.URL, "http"), Token: strings.Repeat("a", 32)}); err != nil {
				t.Fatal(err)
			}
			profiles, err := euiccprofiles.New(agents, euiccprofiles.WithDownloadSafety(catalog, provider))
			if err != nil {
				t.Fatal(err)
			}
			maintenance, err := NewSystemMaintenanceHandler(provider)
			if err != nil {
				t.Fatal(err)
			}
			idle := func(string) (bool, error) { return false, nil }
			server := NewServer(testReplay(t, now), func() time.Time { return now }, WithAgentFacts(agents), WithLineCatalog(catalog, linecatalog.NewHandler(catalog)), WithSystemMaintenance(maintenance), WithAgentRestartBusyCheck(idle), WithEUICCProfiles(profiles))
			store, err := systemupdate.Open(filepath.Join(t.TempDir(), "update"))
			if err != nil {
				t.Fatal(err)
			}
			gate, err := systemupdate.OpenAdmission(store)
			if err != nil {
				t.Fatal(err)
			}
			runtime := UpdateMaintenanceRuntime{Catalog: catalog, Providers: directory, Agents: agentServer, EUICC: profiles, CheckMedia: func() error { return nil }, Checks: []func(string) (bool, error){idle}}
			owner, err := systemupdate.NewMaintenanceOwner(store, gate, runtime, "core-before", strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			post := func(path, body string) *httptest.ResponseRecorder {
				t.Helper()
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				gate.HTTP(server).ServeHTTP(response, request)
				return response
			}
			result := post("/v1/euiccs/"+eid+"/profiles/"+card+"/enable", `{"operation_id":"original-esim","expected_state":"disabled"}`)
			if cleanupOnly && result.Code < 400 || !cleanupOnly && result.Code != http.StatusAccepted {
				t.Fatalf("eSIM setup: %d %s", result.Code, result.Body.String())
			}
			if profiles.UpdateBusy() == nil || (agentServer.HostMaintenanceBusy() != nil) == cleanupOnly {
				t.Fatal("retained eSIM/host precondition not established")
			}
			target := systemupdate.UpdateTarget{RequestedTarget: "2.0.0", ReleaseID: "mdd-2.0.0", SourceRevision: strings.Repeat("c", 40), CoreSHA256: strings.Repeat("b", 64), Contract: 1}
			requestUpdate := func(id string) {
				t.Helper()
				if err := store.Request(systemupdate.Request{OperationID: id, Repository: "example/project", Target: target.RequestedTarget, RequestedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			requestUpdate("update-first")
			view, err := owner.Begin(ctx, "update-first", target)
			if err == nil || view.GuardInstalled || gate.Blocked() || view.Record == nil || view.Record.Phase != systemupdate.MaintenanceReleased {
				t.Fatalf("ESIM_UPDATE_INTERLOCK: rejected update blocked original recovery: %v %+v", err, view)
			}
			provider.mu.Lock()
			lease, posts := provider.lease, provider.updatePosts
			provider.mu.Unlock()
			if lease != "original-esim" || posts != 0 || profiles.UpdateBusy() == nil || (agentServer.HostMaintenanceBusy() != nil) == cleanupOnly {
				t.Fatal("update touched original Provider/host ownership")
			}
			result = post("/v1/euiccs/"+eid+"/refresh", `{"operation_id":"original-readback"}`)
			if result.Code != http.StatusOK || profiles.UpdateBusy() != nil || agentServer.HostMaintenanceBusy() != nil || agents.refreshes != 1 {
				t.Fatalf("original recovery: %d %s", result.Code, result.Body.String())
			}
			wantMutations := 1
			if cleanupOnly {
				wantMutations = 0
			}
			if agents.mutations != wantMutations {
				t.Fatal("recovery retried mutation")
			}
			if err := store.SetStatus(systemupdate.Status{OperationID: "update-first", State: systemupdate.StateFailed, Target: target.RequestedTarget, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			requestUpdate("update-next")
			view, err = owner.Begin(ctx, "update-next", target)
			if err != nil || !view.GuardInstalled || view.Record.Phase != systemupdate.MaintenanceReady {
				t.Fatalf("next update was not admitted: %v %+v", err, view)
			}
			if _, err := owner.Cleanup(ctx, "update-next", view.Record.Revision); err != nil || gate.Blocked() {
				t.Fatalf("next update cleanup: %v", err)
			}
		})
	}
}
