package core

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
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
}

func (m *restartProviderMaintenance) Request(_ context.Context, request providerapply.DrainRequest, begin bool) (providerapply.DrainResult, error) {
	result := providerapply.DrainResult{Ready: true}
	if begin {
		for index, id := range request.LineIDs {
			result.Lines = append(result.Lines, providerapply.DrainLineResult{LineID: id, Code: m.codes[index]})
		}
	} else {
		m.releaseCalls++
		m.released = append([]string(nil), request.LineIDs...)
	}
	return result, nil
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
