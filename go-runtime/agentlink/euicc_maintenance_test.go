package agentlink

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEUICCMaintenanceAllowsOnlyFrozenCommand(t *testing.T) {
	server, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	host := httptest.NewServer(server)
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	executor := &fakeEUICCExecutor{}
	done := make(chan error, 1)
	go func() {
		done <- (Client{URL: strings.Replace(host.URL, "http://", "ws://", 1) + "/agent", Token: testToken,
			Hello: Hello{SchemaVersion: 1, AgentID: "euicc-agent", ProcessGeneration: "process-1"}, Authenticator: &fakeAuthenticator{}, EUICC: executor,
			Health: func() TopologySnapshot {
				return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{
					ReaderName: "reader", CardPresent: true, CardID: "8944000000000000001", SessionGeneration: "insertion-1", IdentityState: CardIdentified,
					EUICC: &EUICCFact{EID: "89049032000000000000000000000001", ProfilesAvailable: true, ProfileManagement: true, Profiles: []EUICCProfileFact{{ICCID: "8944000000000000001", State: EUICCProfileDisabled}}},
				}}}
			}, OperationTimeout: time.Second}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	waitForAgentEUICC(t, server, "euicc-agent", true)
	request := EUICCProfileRequest{OperationID: "switch-b", SessionGeneration: "insertion-1", EID: "89049032000000000000000000000001", ICCID: "8944000000000000001", Action: EUICCProfileEnable, ExpectedState: EUICCProfileDisabled}
	if err := server.BeginHostMaintenance("euicc-agent", "process-1", "switch-b"); err != nil {
		t.Fatal(err)
	}
	permit := WithEUICCProfileMaintenance(ctx, "euicc-agent", "process-1", "switch-b", request)
	for _, scenario := range []string{"ordinary", "operation", "session", "profile", "kind", "lease", "process"} {
		probe := request
		callCtx := permit
		switch scenario {
		case "ordinary":
			callCtx = ctx
		case "operation":
			probe.OperationID = "other"
		case "session":
			probe.SessionGeneration = "other"
		case "profile":
			probe.ICCID = "8944000000000000002"
		case "kind":
			probe.Action = EUICCProfileDisable
			probe.ExpectedState = EUICCProfileEnabled
		case "lease":
			callCtx = WithEUICCProfileMaintenance(ctx, "euicc-agent", "process-1", "other", request)
		case "process":
			callCtx = WithEUICCProfileMaintenance(ctx, "euicc-agent", "old-process", "switch-b", request)
		}
		if _, err := server.ExecuteEUICCProfile(callCtx, "euicc-agent", "process-1", probe); !errors.Is(err, ErrAgentMaintenance) {
			t.Fatalf("%s: %v", scenario, err)
		}
	}
	result, err := server.ExecuteEUICCProfile(permit, "euicc-agent", "process-1", request)
	if err != nil || !result.Changed {
		t.Fatalf("protected dispatch: %+v %v", result, err)
	}
	if err := server.EndHostMaintenance("euicc-agent", "switch-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteEUICCProfile(permit, "euicc-agent", "process-1", request); !errors.Is(err, ErrAgentMaintenance) {
		t.Fatalf("released permit reused: %v", err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.requests) != 1 {
		t.Fatalf("dispatched %d operations", len(executor.requests))
	}
}
