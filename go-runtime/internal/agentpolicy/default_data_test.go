package agentpolicy

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentconnection"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type defaultDataBackend struct {
	stops []agentdata.Target
	err   error
}

func (*defaultDataBackend) PrepareData(context.Context, agentdata.Target, agentdata.Profile) (string, error) {
	return "", errors.New("default policy must not open data")
}

func (*defaultDataBackend) DialData(context.Context, agentdata.Target, string, string) (net.Conn, error) {
	return nil, errors.New("default policy must not borrow data")
}

func (backend *defaultDataBackend) StopData(_ context.Context, target agentdata.Target) error {
	backend.stops = append(backend.stops, target)
	return backend.err
}

func TestDefaultDataReconcileRequiresObservedDisconnect(t *testing.T) {
	for _, test := range []struct {
		name       string
		data       agentmodem.DataState
		busy       bool
		missing    bool
		replaced   bool
		generation string
		stopErr    error
		stops      int
		code       string
	}{
		{name: "disconnected", data: agentmodem.DataDisconnected, code: "policy_ready"},
		{name: "connected", data: agentmodem.DataConnected, stops: 1, code: "default_data_disconnect_unconfirmed"},
		{name: "connecting", data: agentmodem.DataConnecting, stops: 1, code: "default_data_disconnect_unconfirmed"},
		{name: "unknown", data: agentmodem.DataUnknown, code: "cellular_bearer_unconfirmed"},
		{name: "omitted", code: "cellular_bearer_unconfirmed"},
		{name: "busy", data: agentmodem.DataConnected, busy: true, code: "data_lease_active"},
		{name: "no runtime", data: agentmodem.DataConnected, missing: true, code: "default_data_disconnect_unavailable"},
		{name: "replaced", data: agentmodem.DataConnected, replaced: true, code: "default_data_disconnect_failed"},
		{name: "no generation", data: agentmodem.DataConnected, generation: "missing", code: "modem_target_replaced"},
		{name: "backend error", data: agentmodem.DataConnected, stopErr: errors.New("platform stop failed"), stops: 1, code: "default_data_disconnect_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, runtime, coordinator := testManager(t)
			now := time.Unix(1000, 0)
			manager.config.Now = func() time.Time { return now }
			manager.config.Recovery = recovery.Policy{Base: time.Second, Cap: 8 * time.Second}
			fact := runtime.facts[0]
			fact.Network.Data = test.data
			if test.generation == "missing" {
				fact.SIM.SessionGeneration = ""
			}
			runtime.facts[0] = fact
			if test.replaced {
				runtime.facts[0].SIM.SessionGeneration = "replacement-session"
			}
			coordinator.setActive(test.busy)
			backend := &defaultDataBackend{err: test.stopErr}
			if !test.missing {
				connection, err := agentconnection.New(backend)
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.BindConnection(connection); err != nil {
					t.Fatal(err)
				}
			}
			manager.observeDiscovery([]agentmodem.Fact{fact})
			discovery, _, err := manager.DiscoveryFor(fact.EquipmentID)
			if err != nil {
				t.Fatal(err)
			}
			manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
			view := manager.View(fact.EquipmentID, fact.SIM.ICCID)
			if view.Code != test.code || (view.State == "ready") != (test.data == agentmodem.DataDisconnected) || len(backend.stops) != test.stops {
				t.Fatalf("view=%+v stops=%v", view, backend.stops)
			}
			for _, target := range backend.stops {
				if target != (agentdata.Target{AttachmentID: fact.AttachmentID, EquipmentID: fact.EquipmentID,
					CardID: fact.SIM.ICCID, SIMSessionGeneration: fact.SIM.SessionGeneration}) {
					t.Fatalf("stopped another target: %+v", target)
				}
			}
			policy, found, err := manager.config.Store.Get(fact.EquipmentID, fact.SIM.ICCID)
			after, _, discoveryErr := manager.DiscoveryFor(fact.EquipmentID)
			if err != nil || discoveryErr != nil || found || policy != Default(fact.EquipmentID, fact.SIM.ICCID) ||
				view.Persisted || view.Revision != 0 || runtime.radioCalls != 0 || runtime.saves != 0 || !reflect.DeepEqual(discovery, after) {
				t.Fatalf("default changed durable intent, enrollment or radio: policy=%+v found=%t errors=%v/%v", policy, found, err, discoveryErr)
			}
			if test.data == agentmodem.DataDisconnected {
				manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
				if len(backend.stops) != 0 {
					t.Fatal("disconnected device received another stop")
				}
				return
			}
			if !view.RetryAt.After(now) {
				t.Fatal("unconfirmed default has no retry deadline")
			}
			manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
			if next := manager.View(fact.EquipmentID, fact.SIM.ICCID); next.RetryAt != view.RetryAt || len(backend.stops) != test.stops {
				t.Fatal("ordinary observation repeated stop or advanced backoff")
			}
			// StopData deliberately leaves the bearer connected, like a no-owner backend.
			now = view.RetryAt
			manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
			next := manager.View(fact.EquipmentID, fact.SIM.ICCID)
			if next.State == "ready" || next.RetryAt.Sub(now) <= view.RetryAt.Sub(time.Unix(1000, 0)) || len(backend.stops) != 2*test.stops {
				t.Fatalf("unconfirmed stop did not back off: next=%+v stops=%d", next, len(backend.stops))
			}
			now = next.RetryAt
			fact.Network.Data = agentmodem.DataDisconnected
			if test.generation == "missing" {
				fact.SIM.SessionGeneration = "reacquired-session"
			}
			manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
			if final := manager.View(fact.EquipmentID, fact.SIM.ICCID); final.State != "ready" || !final.RetryAt.IsZero() || len(backend.stops) != 2*test.stops {
				t.Fatalf("fresh disconnect did not settle default: %+v stops=%d", final, len(backend.stops))
			}
		})
	}
}

type defaultDataGate struct {
	entered chan struct{}
	release chan struct{}
}

func (gate defaultDataGate) DoAuxiliary(ctx context.Context, _ string, callback func(context.Context) error) error {
	close(gate.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate.release:
		return callback(ctx)
	}
}

func TestDefaultDataReconcileDoesNotOverridePolicySavedWhileWaiting(t *testing.T) {
	manager, runtime, _ := testManager(t)
	fact := runtime.facts[0]
	fact.Network.Data = agentmodem.DataConnected
	runtime.facts[0] = fact
	backend := &defaultDataBackend{}
	connection, err := agentconnection.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.BindConnection(connection); err != nil {
		t.Fatal(err)
	}
	gate := defaultDataGate{entered: make(chan struct{}), release: make(chan struct{})}
	if err := manager.BindCoordinator(gate); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.ReconcilePolicies(ctx, []agentmodem.Fact{fact})
	}()
	defer func() {
		cancel()
		<-done
	}()
	select {
	case <-gate.entered:
	case <-done:
		t.Fatal("default reconcile did not enter the guarded disconnect path")
	case <-ctx.Done():
		t.Fatal("default reconcile did not reach the lifecycle gate")
	}
	policy := Default(fact.EquipmentID, fact.SIM.ICCID)
	policy.Desired.ConnectionEnabled, policy.Desired.RoamingEnabled = true, true
	stored, err := manager.config.Store.PutExpected(policy, 0)
	close(gate.release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	after, found, err := manager.config.Store.Get(fact.EquipmentID, fact.SIM.ICCID)
	if err != nil || !found || after != stored || len(backend.stops) != 0 || runtime.radioCalls != 0 {
		t.Fatalf("default overrode newly stored intent: policy=%+v stops=%v err=%v", after, backend.stops, err)
	}
}
