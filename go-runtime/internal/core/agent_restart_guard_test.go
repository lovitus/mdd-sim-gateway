package core

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/euiccprofiles"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type restartMaintenanceAgents struct {
	fixedAgentFacts
	begins, ends int
}

func (a *restartMaintenanceAgents) BeginHostMaintenance(string, string, string) error {
	a.begins++
	return nil
}
func (a *restartMaintenanceAgents) EndHostMaintenance(string, string) error { a.ends++; return nil }

type restartProviderMaintenance struct {
	codes        []string
	released     []string
	releaseCalls int
	catalog      *linecatalog.Store
	lease        string
	active       map[string]bool
	failRelease  bool
}

func (m *restartProviderMaintenance) Snapshot(context.Context) (providerapply.Snapshot, error) {
	revision := uint64(1)
	if m.catalog != nil {
		snapshot, err := m.catalog.Snapshot()
		if err != nil {
			return providerapply.Snapshot{}, err
		}
		revision = snapshot.Revision
	}
	result := providerapply.Snapshot{SchemaVersion: 1, CatalogRevision: revision}
	for id, active := range m.active {
		status := vowifiipc.MaintenanceStatus{}
		if active {
			status = vowifiipc.MaintenanceStatus{Draining: true, Code: "apply_drain", LeaseID: m.lease}
		}
		result.Lines = append(result.Lines, providerapply.LineStatus{LineID: id, Code: "provider_reachable", ProviderPresent: true, ProcessGeneration: "provider-" + id, Maintenance: status})
	}
	return result, nil
}

func (*restartProviderMaintenance) Status(context.Context, string) (vowifiipc.Snapshot, error) {
	return vowifiipc.Snapshot{Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeStopped}}, nil
}

func (m *restartProviderMaintenance) Request(_ context.Context, request providerapply.DrainRequest, begin bool) (providerapply.DrainResult, error) {
	result := providerapply.DrainResult{SchemaVersion: 1, CatalogRevision: request.CatalogRevision, LeaseID: request.LeaseID, Ready: true}
	if begin {
		m.lease = request.LeaseID
		m.active = map[string]bool{}
		for index, id := range request.LineIDs {
			result.Lines = append(result.Lines, providerapply.DrainLineResult{LineID: id, Code: m.codes[index], ProcessGeneration: "provider-" + id})
			if m.codes[index] == "drained" {
				m.active[id] = true
			}
		}
	} else {
		m.releaseCalls++
		if m.failRelease {
			m.failRelease = false
			return providerapply.DrainResult{}, errors.New("resume unavailable")
		}
		if m.catalog != nil {
			current, _ := m.catalog.Snapshot()
			if current.Revision != request.CatalogRevision {
				return providerapply.DrainResult{}, errors.New("catalog revision changed")
			}
		}
		m.released = append([]string(nil), request.LineIDs...)
		for _, id := range request.LineIDs {
			m.active[id] = false
			result.Lines = append(result.Lines, providerapply.DrainLineResult{LineID: id, Code: "resumed", ProcessGeneration: "provider-" + id})
		}
	}
	return result, nil
}

type profileGuardAgents struct {
	euiccprofiles.AgentRuntime
	restartMaintenanceAgents
	afterBegin func()
	commands   []agentlink.EUICCProfileRequest
}

func (a *profileGuardAgents) Statuses() []agentlink.ConnectionStatus {
	return a.fixedAgentFacts.statuses
}
func (a *profileGuardAgents) BeginHostMaintenance(agent, generation, lease string) error {
	err := a.restartMaintenanceAgents.BeginHostMaintenance(agent, generation, lease)
	if a.afterBegin != nil {
		a.afterBegin()
	}
	return err
}
func (a *profileGuardAgents) ExecuteEUICCProfile(_ context.Context, _, _ string, r agentlink.EUICCProfileRequest) (agentlink.EUICCProfileResponse, error) {
	a.commands = append(a.commands, r)
	return agentlink.EUICCProfileResponse{OperationID: r.OperationID, EID: r.EID, ICCID: r.ICCID, SessionGeneration: r.SessionGeneration, Action: r.Action, Outcome: agentlink.EUICCProfileRefreshPending, Changed: true}, nil
}
func (a *profileGuardAgents) ExecuteEUICCProfileCommand(_ context.Context, c agentlink.EUICCProfileCommand) (agentlink.EUICCProfileResponse, error) {
	a.commands = append(a.commands, agentlink.EUICCProfileRequest{Action: c.Action})
	return agentlink.EUICCProfileResponse{}, errors.New("readback temporarily unavailable")
}

func TestProfileCleanupUsesRealCoreGuardAfterCatalogChange(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "revision", true: "release-retry"}[fail], func(t *testing.T) {
			now := time.Now()
			catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer catalog.Close()
			const eid = "89049032000000000000000000000001"
			const card = "8944100000000000001"
			line := deviceTestLine("line-a", card, "")
			if _, err = catalog.Put(line); err != nil {
				t.Fatal(err)
			}
			status := agentlink.ConnectionStatus{AgentID: "agent", ProcessGeneration: "process", LastReport: now, LastSeen: now, Topology: &agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{{ReaderName: "reader", SessionGeneration: "insert", CardPresent: true, CardID: card, IdentityState: agentlink.CardIdentified, EUICC: &agentlink.EUICCFact{EID: eid, InventoryRefresh: true, ProfilesAvailable: true, ProfileManagement: true, Profiles: []agentlink.EUICCProfileFact{{ICCID: card, State: agentlink.EUICCProfileDisabled}}}}}}}
			agents := &profileGuardAgents{restartMaintenanceAgents: restartMaintenanceAgents{fixedAgentFacts: fixedAgentFacts{statuses: []agentlink.ConnectionStatus{status}}}}
			agents.afterBegin = func() {
				line.Name = "updated"
				if _, err := catalog.Put(line); err != nil {
					t.Fatal(err)
				}
			}
			providers := &restartProviderMaintenance{codes: []string{"drained"}, catalog: catalog, failRelease: fail}
			maintenance, _ := NewSystemMaintenanceHandler(providers)
			server := NewServer(testReplay(t, now), func() time.Time { return now }, WithAgentFacts(agents), WithLineCatalog(catalog, linecatalog.NewHandler(catalog)), WithSystemMaintenance(maintenance), WithAgentRestartBusyCheck(func(string) (bool, error) { return false, nil }))
			service, err := euiccprofiles.New(agents, euiccprofiles.WithDownloadSafety(catalog, providers))
			if err != nil {
				t.Fatal(err)
			}
			service.SetMutationGuard(server.prepareAgentMaintenance)
			mux := http.NewServeMux()
			mux.Handle("POST /v1/euiccs/{eid}/profiles/{iccid}/{action}", service)
			mux.Handle("POST /v1/euiccs/{eid}/refresh", service)
			request := httptest.NewRequest("POST", "/v1/euiccs/"+eid+"/profiles/"+card+"/enable", bytes.NewBufferString(`{"operation_id":"switch-b","expected_state":"disabled"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code < 400 || len(agents.commands) != 0 {
				t.Fatalf("catalog race dispatched mutation: %d %v", response.Code, agents.commands)
			}
			if fail {
				if agents.ends != 0 {
					t.Fatal("PROFILE_CLEANUP: host released before provider handback")
				}
				request = httptest.NewRequest("POST", "/v1/euiccs/"+eid+"/refresh", bytes.NewBufferString(`{"operation_id":"read-original"}`))
				request.Header.Set("Content-Type", "application/json")
				mux.ServeHTTP(httptest.NewRecorder(), request)
			}
			if agents.ends != 1 || providers.active["line-a"] {
				t.Fatalf("PROFILE_CLEANUP: stranded permit ends=%d active=%v", agents.ends, providers.active)
			}
			for _, request := range agents.commands {
				if request.Action != agentlink.EUICCProfileRefresh {
					t.Fatal("recovery repeated mutation")
				}
			}
		})
	}
}

func TestRestartReleasesOnlyProvidersActuallyDrained(t *testing.T) {
	for _, codes := range [][]string{{"provider_absent", "provider_absent"}, {"drained", "provider_absent"}, {"drained", "drained"}} {
		t.Run(codes[0]+"-"+codes[1], func(t *testing.T) {
			now := time.Now()
			catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer catalog.Close()
			status := agentlink.ConnectionStatus{AgentID: "agent", ProcessGeneration: "process", LastReport: now, LastSeen: now,
				Topology: &agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady}}
			for index, card := range []string{"8944100000000000001", "8944100000000000002"} {
				id := []string{"line-a", "line-b"}[index]
				if _, err := catalog.Put(deviceTestLine(id, card, "")); err != nil {
					t.Fatal(err)
				}
				status.Topology.Readers = append(status.Topology.Readers, agentlink.ReaderFact{ReaderName: id, CardPresent: true,
					SessionGeneration: id, CardID: card, IdentityState: agentlink.CardIdentified})
			}
			agents := &restartMaintenanceAgents{fixedAgentFacts: fixedAgentFacts{statuses: []agentlink.ConnectionStatus{status}}}
			providers := &restartProviderMaintenance{codes: codes}
			maintenance, _ := NewSystemMaintenanceHandler(providers)
			server := NewServer(testReplay(t, now), func() time.Time { return now }, WithAgentFacts(agents),
				WithLineCatalog(catalog, linecatalog.NewHandler(catalog)), WithSystemMaintenance(maintenance),
				WithAgentRestartBusyCheck(func(string) (bool, error) { return false, nil }))
			release, err := server.prepareAgentRestart(context.Background(), status, "restart-test")
			if err != nil {
				t.Fatal(err)
			}
			if err := release(context.Background()); err != nil {
				t.Fatal(err)
			}
			var want []string
			for i, code := range codes {
				if code == "drained" {
					want = append(want, []string{"line-a", "line-b"}[i])
				}
			}
			if !reflect.DeepEqual(providers.released, want) || agents.begins != 1 || agents.ends != 1 {
				t.Fatalf("released %v want %v", providers.released, want)
			}
			if len(want) == 0 && providers.releaseCalls != 0 {
				t.Fatal("attempted resume for absent providers")
			}
		})
	}
}
