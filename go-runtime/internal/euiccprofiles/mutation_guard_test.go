package euiccprofiles

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestProfileSwitchProtectsWholeEIDAndFrozenInsertion(t *testing.T) {
	for _, scenario := range []string{"active-other-disabled-line", "active-call", "stale", "ambiguous", "unknown-inventory", "changed-after-permit", "stopped"} {
		t.Run(scenario, func(t *testing.T) {
			agents := &fakeAgents{result: agentlink.EUICCProfileResponse{Outcome: agentlink.EUICCProfileRefreshPending, Changed: true}}
			s := mutationService(agents)
			const other = "8944000000000000002"
			agents.statuses[0].Topology.Readers[0].EUICC.Profiles = append(agents.statuses[0].Topology.Readers[0].EUICC.Profiles, agentlink.EUICCProfileFact{ICCID: other, State: agentlink.EUICCProfileEnabled})
			s.catalog = fakeCatalog{snapshot: linecatalog.Snapshot{Revision: 1, Lines: []linecatalog.Line{{ID: "line-a", CardID: other, Enabled: false}}}}
			status := vowifiipc.Snapshot{Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeStopped}}
			acquired, released := 0, 0
			s.SetMutationGuard(func(_ context.Context, target agentlink.ConnectionStatus, lines []string, id string) (func(context.Context) error, error) {
				acquired++
				if len(lines) != 1 || lines[0] != "line-a" || target.AgentID != "agent-a" || id != "switch-b" {
					t.Fatalf("wrong permit target: %+v %v %s", target, lines, id)
				}
				if scenario == "changed-after-permit" {
					agents.statuses[0].Topology.Readers[0].SessionGeneration = "new-insertion"
				}
				return func(context.Context) error { released++; return nil }, nil
			})
			switch scenario {
			case "active-other-disabled-line":
				status.Runtime.Condition = vowifiipc.RuntimeRunning
			case "active-call":
				status.ActiveCall = &vowifiipc.ActiveCall{}
			case "stale":
				agents.statuses[0].LastReport = time.Now().Add(-time.Minute)
			case "ambiguous":
				agents.statuses = append(agents.statuses, agents.statuses[0])
			case "unknown-inventory":
				agents.statuses[0].Topology.Readers[0].EUICC.ProfilesAvailable = false
			}
			s.providers = fakeProviderStatus{statuses: map[string]vowifiipc.Snapshot{"line-a": status}}
			mux := http.NewServeMux()
			mux.Handle("POST /v1/euiccs/{eid}/profiles/{iccid}/{action}", s)
			result := post(t, mux, "/v1/euiccs/"+testEID+"/profiles/"+testICCID+"/enable", map[string]any{"operation_id": "switch-b", "expected_state": "disabled"})
			if scenario == "stopped" {
				if result.Code != 202 || len(agents.commands) != 1 || acquired != 1 || released != 1 {
					t.Fatalf("allowed path: %d %s commands=%d permits=%d/%d", result.Code, result.Body, len(agents.commands), acquired, released)
				}
			} else if result.Code < 400 || len(agents.commands) != 0 || acquired != released {
				t.Fatalf("unsafe switch: %d %s commands=%d permits=%d/%d", result.Code, result.Body, len(agents.commands), acquired, released)
			}
		})
	}
}

func TestProfileUnknownRetainsPermitUntilExactReadbackWithoutRewrite(t *testing.T) {
	agents := &fakeAgents{err: errors.New("connection lost after dispatch")}
	s := mutationService(agents)
	released := 0
	s.SetMutationGuard(func(context.Context, agentlink.ConnectionStatus, []string, string) (func(context.Context) error, error) {
		return func(context.Context) error { released++; return nil }, nil
	})
	command := agentlink.EUICCProfileCommand{OperationID: "switch-b", EID: testEID, ICCID: testICCID, Action: agentlink.EUICCProfileEnable, ExpectedState: agentlink.EUICCProfileDisabled}
	if _, err := s.mutateProfile(context.Background(), command); err == nil || released != 0 {
		t.Fatal("unknown request released protection")
	}
	command.OperationID = "new-write"
	if _, err := s.mutateProfile(context.Background(), command); err == nil || len(agents.commands) != 1 {
		t.Fatal("unknown mutation was resent")
	}
	agents.err = nil
	agents.result = agentlink.EUICCProfileResponse{Outcome: agentlink.EUICCProfileRefreshed, Inventory: cloneEUICC(agents.statuses[0].Topology.Readers[0].EUICC)}
	agents.statuses[0].Topology.Readers[0].SessionGeneration = "post-refresh"
	if _, err := s.refreshProfile(context.Background(), agentlink.EUICCProfileCommand{OperationID: "read-original", EID: testEID, Action: agentlink.EUICCProfileRefresh}); err != nil {
		t.Fatal(err)
	}
	if released != 1 || len(agents.commands) != 2 || agents.commands[1].Action != agentlink.EUICCProfileRefresh {
		t.Fatalf("unsafe recovery: releases=%d commands=%+v", released, agents.commands)
	}
}
