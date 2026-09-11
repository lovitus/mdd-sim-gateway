package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

type restartRuntimeFixture struct {
	status agentlink.ConnectionStatus
	calls  int
}

func (f *restartRuntimeFixture) Statuses() []agentlink.ConnectionStatus {
	return []agentlink.ConnectionStatus{f.status}
}
func (f *restartRuntimeFixture) Status(string) (agentlink.ConnectionStatus, bool) {
	return f.status, true
}
func (f *restartRuntimeFixture) RequestAgentRestart(_ context.Context, _ string, r agentlink.AgentRestartRequest) (agentlink.AgentRestartResponse, error) {
	f.calls++
	return agentlink.AgentRestartResponse{OperationID: r.OperationID, ProcessGeneration: r.ProcessGeneration, Accepted: true}, nil
}

type restartGuardFixture struct {
	denied   bool
	released int
}

func (f *restartGuardFixture) Prepare(context.Context, agentlink.ConnectionStatus, string) (func(context.Context) error, error) {
	if f.denied {
		return nil, errors.New("active call")
	}
	return func(context.Context) error { f.released++; return nil }, nil
}

func TestAgentRestartRequiresGuardAndNewReportedGeneration(t *testing.T) {
	for _, scenario := range []string{"busy", "unchanged", "restarted"} {
		t.Run(scenario, func(t *testing.T) {
			runtime := &restartRuntimeFixture{status: agentlink.ConnectionStatus{AgentID: "agent", ProcessGeneration: "old", Capabilities: []string{agentlink.AgentRestartFeature}}}
			guard := &restartGuardFixture{denied: scenario == "busy"}
			handler, err := NewAgentRestartHandler(runtime, guard)
			if err != nil {
				t.Fatal(err)
			}
			handler.wait = func(context.Context, time.Duration) error {
				if scenario == "restarted" {
					runtime.status.ProcessGeneration = "new"
					runtime.status.Topology = &agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady}
					runtime.status.LastReport = time.Now()
				}
				return nil
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/agents/agent/restart", strings.NewReader(`{"operation_id":"restart-test","process_generation":"old"}`))
			request.SetPathValue("agentID", "agent")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			want := http.StatusServiceUnavailable
			if scenario == "busy" {
				want = http.StatusConflict
			}
			if scenario == "restarted" {
				want = http.StatusOK
			}
			if response.Code != want {
				t.Fatalf("response: %d %s", response.Code, response.Body.String())
			}
			if scenario == "busy" {
				if runtime.calls != 0 || guard.released != 0 {
					t.Fatal("busy guard allowed restart")
				}
			} else if runtime.calls != 1 || guard.released != 1 {
				t.Fatal("restart or release count is wrong")
			}
		})
	}
}
