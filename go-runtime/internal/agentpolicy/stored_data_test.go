package agentpolicy

import (
	"context"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentconnection"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func TestStoredDataPolicyRequiresGenerationAndObservedDisconnect(t *testing.T) {
	for _, tc := range []struct {
		name              string
		data              agentmodem.DataState
		missingGeneration bool
		flight            bool
		intermediate      []agentmodem.DataState
	}{
		{"connected", agentmodem.DataConnected, false, false, nil},
		{"connecting", agentmodem.DataConnecting, false, false, nil},
		{"flight_mode", agentmodem.DataConnected, false, true, nil},
		{"unowned_connected", agentmodem.DataConnected, true, false, nil},
		{"unowned_disconnected", agentmodem.DataDisconnected, true, false, nil},
		{"connected_unknown", agentmodem.DataConnected, false, false, []agentmodem.DataState{agentmodem.DataUnknown}},
		{"connected_disconnecting", agentmodem.DataConnected, false, false, []agentmodem.DataState{agentmodem.DataDisconnecting}},
		{"connected_missing", agentmodem.DataConnected, false, false, []agentmodem.DataState{""}},
		{"flight_unknown", agentmodem.DataConnected, false, true, []agentmodem.DataState{agentmodem.DataUnknown}},
		{"flight_disconnecting", agentmodem.DataConnected, false, true, []agentmodem.DataState{agentmodem.DataDisconnecting}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, runtime, _ := testManager(t)
			now := time.Unix(1000, 0)
			manager.config.Now = func() time.Time { return now }
			fact := runtime.facts[0]
			generation := fact.SIM.SessionGeneration
			fact.Network.Data = tc.data
			if tc.missingGeneration {
				fact.SIM.SessionGeneration = ""
			}
			runtime.facts[0] = fact
			policy := Default(fact.EquipmentID, fact.SIM.ICCID)
			policy.Desired.FlightMode = tc.flight
			stored, err := manager.config.Store.PutExpected(policy, 0)
			if err != nil {
				t.Fatal(err)
			}
			backend := &defaultDataBackend{}
			connection, err := agentconnection.New(backend)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.BindConnection(connection); err != nil {
				t.Fatal(err)
			}
			manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
			view := manager.View(fact.EquipmentID, fact.SIM.ICCID)
			code := "cellular_data_disconnect_unconfirmed"
			if tc.missingGeneration {
				code = "modem_target_replaced"
			}
			if view.State == "ready" || view.Code != code || !view.RetryAt.After(now) {
				t.Fatalf("false readiness: %+v", view)
			}
			if tc.missingGeneration && (len(backend.stops) != 0 || runtime.radioCalls != 0) {
				t.Fatal("unowned device was mutated")
			}
			stops := len(backend.stops)
			manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
			if next := manager.View(fact.EquipmentID, fact.SIM.ICCID); next.RetryAt != view.RetryAt || len(backend.stops) != stops {
				t.Fatal("observation advanced backoff or repeated stop")
			}
			for _, intermediate := range tc.intermediate {
				now = view.RetryAt
				fact.Network.Data = intermediate
				if tc.flight {
					fact.Network.SoftwareRadio = agentmodem.RadioOff
				}
				runtime.facts[0] = fact
				manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
				view = manager.View(fact.EquipmentID, fact.SIM.ICCID)
				if view.State == "ready" || view.Code != "cellular_data_disconnect_unconfirmed" || !view.RetryAt.After(now) {
					t.Fatalf("became ready without observing disconnect (%q): %+v", intermediate, view)
				}
				stops = len(backend.stops)
				manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
				if next := manager.View(fact.EquipmentID, fact.SIM.ICCID); next.RetryAt != view.RetryAt || len(backend.stops) != stops {
					t.Fatal("transition observation bypassed backoff")
				}
			}
			now = view.RetryAt
			fact.Network.Data, fact.SIM.SessionGeneration = agentmodem.DataDisconnected, generation
			if tc.flight {
				fact.Network.SoftwareRadio = agentmodem.RadioOff
			}
			runtime.facts[0] = fact
			manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
			if next := manager.View(fact.EquipmentID, fact.SIM.ICCID); next.State != "ready" || next.Code != "policy_ready" || !next.RetryAt.IsZero() {
				t.Fatalf("did not converge: %+v", next)
			}
			after, found, err := manager.config.Store.Get(fact.EquipmentID, fact.SIM.ICCID)
			if err != nil || !found || after != stored {
				t.Fatal("reconcile changed user intent")
			}
		})
	}
}
