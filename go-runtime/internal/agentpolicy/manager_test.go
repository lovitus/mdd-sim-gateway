package agentpolicy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type testRuntime struct {
	mu           sync.Mutex
	facts        []agentmodem.Fact
	radioCalls   int
	saves        int
	saveFailures int
}

func (runtime *testRuntime) Probe(context.Context) ([]agentmodem.Fact, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	result := make([]agentmodem.Fact, len(runtime.facts))
	copy(result, runtime.facts)
	return result, nil
}
func (runtime *testRuntime) SetPolicyRadio(_ context.Context, _ Target, enabled bool) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.radioCalls++
	if enabled {
		runtime.facts[0].Network.SoftwareRadio = agentmodem.RadioOn
	} else {
		runtime.facts[0].Network.SoftwareRadio = agentmodem.RadioOff
	}
	return nil
}
func (*testRuntime) ListPolicyProfiles(context.Context, Target) ([]ProfileView, error) {
	return []ProfileView{}, nil
}
func (runtime *testRuntime) SavePolicyProfile(context.Context, Target, Profile) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.saves++
	if runtime.saveFailures > 0 {
		runtime.saveFailures--
		return errors.New("injected profile apply failure")
	}
	return nil
}
func (*testRuntime) PolicyProfileMode() string { return "agent" }

type lifecycleCoordinator struct {
	mu     sync.Mutex
	active bool
}

type passthroughCoordinator struct{}

func (passthroughCoordinator) DoAuxiliary(ctx context.Context, _ string, callback func(context.Context) error) error {
	return callback(ctx)
}

type blockingDataBackend struct {
	entered  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	prepares int
	stops    int
}

func (backend *blockingDataBackend) PrepareData(ctx context.Context, _ agentdata.Target, profile agentdata.Profile) (string, error) {
	backend.mu.Lock()
	backend.prepares++
	backend.mu.Unlock()
	select {
	case <-backend.entered:
	default:
		close(backend.entered)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-backend.release:
	}
	return profile.Name, nil
}
func (*blockingDataBackend) DialData(context.Context, agentdata.Target, string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}
func (backend *blockingDataBackend) StopData(context.Context, agentdata.Target) error {
	backend.mu.Lock()
	backend.stops++
	backend.mu.Unlock()
	return nil
}

func (coordinator *lifecycleCoordinator) DoAuxiliary(ctx context.Context, _ string, callback func(context.Context) error) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.active {
		return agentdata.ErrSessionActive
	}
	return callback(ctx)
}
func (coordinator *lifecycleCoordinator) setActive(value bool) {
	coordinator.mu.Lock()
	coordinator.active = value
	coordinator.mu.Unlock()
}

func testManager(t *testing.T) (*Manager, *testRuntime, *lifecycleCoordinator) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "policy.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &testRuntime{facts: []agentmodem.Fact{{AttachmentID: "attachment-a", EquipmentID: "862547055201716",
		SIM:     agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "8985200000000000001", SessionGeneration: "session-a"},
		Network: agentmodem.NetworkFact{Registration: agentmodem.RegistrationHome, SoftwareRadio: agentmodem.RadioOn}}}}
	coordinator := &lifecycleCoordinator{}
	manager, err := New(Config{Store: store, Runtime: runtime, Coordinator: coordinator,
		Recovery: recovery.Policy{Base: time.Millisecond, Cap: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager, runtime, coordinator
}

func TestPolicyMutationIsAtomicWithDataLeaseAndLeavesNoSideEffect(t *testing.T) {
	manager, runtime, coordinator := testManager(t)
	coordinator.setActive(true)
	request := agentlink.ModemPolicyRequest{OperationID: "policy-off", AttachmentID: "attachment-a",
		EquipmentID: "862547055201716", CardID: "8985200000000000001", SIMSessionGeneration: "session-a",
		Action: agentlink.ModemPolicySet, ExpectedRevision: 0, Patch: agentlink.ModemPolicyPatch{FlightMode: boolPointer(true)}}
	blocked := manager.Execute(context.Background(), request)
	if blocked.Failure == nil || blocked.Failure.Code != "data_lease_active" {
		t.Fatalf("blocked=%+v", blocked)
	}
	policy, found, err := manager.config.Store.Get(request.EquipmentID, request.CardID)
	if err != nil || found || policy.Revision != 0 || runtime.radioCalls != 0 {
		t.Fatalf("policy=%+v found=%t radio=%d err=%v", policy, found, runtime.radioCalls, err)
	}
	coordinator.setActive(false)
	applied := manager.Execute(context.Background(), request)
	if applied.Failure != nil || applied.Policy == nil || applied.Policy.Revision != 1 || runtime.radioCalls != 1 {
		t.Fatalf("applied=%+v radio=%d", applied, runtime.radioCalls)
	}
}

func TestBorrowAdmissionRequiresPersistentPolicyAndFreshRoaming(t *testing.T) {
	manager, runtime, _ := testManager(t)
	target := agentdata.Target{AttachmentID: "attachment-a", EquipmentID: "862547055201716", CardID: "8985200000000000001"}
	if _, err := manager.ResolveDataProfile(context.Background(), target, "", "lease-a", "egress:gb"); !errors.Is(err, ErrCellularDisabled) {
		t.Fatalf("disabled err=%v", err)
	}
	policy, _, _ := manager.config.Store.Get(target.EquipmentID, target.CardID)
	policy.Desired.CellularEnabled = true
	policy, err := manager.config.Store.PutExpected(policy, 0)
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.facts[0].Network.Registration = agentmodem.RegistrationRoaming
	runtime.mu.Unlock()
	if _, err := manager.ResolveDataProfile(context.Background(), target, "", "lease-a", "egress:gb"); !errors.Is(err, ErrRoamingDisabled) {
		t.Fatalf("roaming err=%v", err)
	}
	policy.Desired.RoamingEnabled = true
	if _, err = manager.config.Store.PutExpected(policy, 1); err != nil {
		t.Fatal(err)
	}
	profile, err := manager.ResolveDataProfile(context.Background(), target, "", "lease-a", "egress:gb")
	if err != nil || !profile.AllowRoaming {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
}

func TestSameCardReinsertRejectsOldPolicyAndDataGenerationWithoutHardwareSideEffects(t *testing.T) {
	t.Run("policy", func(t *testing.T) {
		manager, runtime, _ := testManager(t)
		runtime.mu.Lock()
		runtime.facts[0].SIM.SessionGeneration = "session-b"
		runtime.mu.Unlock()
		response := manager.Execute(context.Background(), policyRequest(0,
			agentlink.ModemPolicyPatch{FlightMode: boolPointer(true)}))
		if response.Failure == nil || response.Failure.Code != "modem_target_replaced" {
			t.Fatalf("stale policy request=%+v", response)
		}
		stored, found, err := manager.config.Store.Get("862547055201716", "8985200000000000001")
		if err != nil || found || stored.Revision != 0 || runtime.radioCalls != 0 || runtime.saves != 0 {
			t.Fatalf("stale policy side effects policy=%+v found=%t radio=%d saves=%d err=%v",
				stored, found, runtime.radioCalls, runtime.saves, err)
		}
	})

	t.Run("data-prepare", func(t *testing.T) {
		manager, runtime, _ := testManager(t)
		stored, _, err := manager.config.Store.Get("862547055201716", "8985200000000000001")
		if err != nil {
			t.Fatal(err)
		}
		stored.Desired.CellularEnabled = true
		if _, err := manager.config.Store.PutExpected(stored, 0); err != nil {
			t.Fatal(err)
		}
		runtime.mu.Lock()
		runtime.facts[0].SIM.SessionGeneration = "session-b"
		runtime.mu.Unlock()
		backend := &blockingDataBackend{entered: make(chan struct{}), release: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		data, err := agentdata.NewManager(agentdata.Config{
			Context: ctx, ServerURL: "http://127.0.0.1:1", ServerToken: "0123456789abcdef0123456789abcdef",
			AgentID: "agent-a", ProcessGeneration: "process-a", HTTPClient: http.DefaultClient,
			Backend: backend, Coordinator: passthroughCoordinator{}, Admission: manager,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer data.Close()
		if err := manager.BindCoordinator(data); err != nil {
			t.Fatal(err)
		}
		response := data.ExecuteModemData(context.Background(), agentlink.ModemDataRequest{
			OperationID: "stale-data-prepare", AttachmentID: "attachment-a", EquipmentID: "862547055201716",
			CardID: "8985200000000000001", SIMSessionGeneration: "session-a",
			Action: agentlink.ModemDataPrepare, SessionID: "stale-lease", Purpose: "egress:gb",
			ExpiresAt: time.Now().UTC().Add(time.Minute), MaxBytes: 1 << 20,
		})
		backend.mu.Lock()
		prepares, stops := backend.prepares, backend.stops
		backend.mu.Unlock()
		if response.Failure == nil || response.Failure.Code != "modem_data_target_replaced" ||
			prepares != 0 || stops != 0 || manager.DataLeaseActive("862547055201716") {
			t.Fatalf("stale data response=%+v prepares=%d stops=%d lease=%t", response, prepares, stops,
				manager.DataLeaseActive("862547055201716"))
		}
	})
}

func TestProfileFailureStaysRecoveringUntilFreshReconcileAppliesIt(t *testing.T) {
	manager, runtime, _ := testManager(t)
	now := time.Now().UTC()
	manager.config.Now = func() time.Time { return now }
	policy, err := manager.config.Store.SaveProfileExpected("862547055201716", "8985200000000000001",
		Profile{Name: "carrier", APN: "internet", Auth: "NONE"}, false, 0)
	if err != nil || policy.Desired.SelectedProfile != "carrier" {
		t.Fatalf("profile seed=%+v err=%v", policy, err)
	}
	runtime.mu.Lock()
	runtime.saveFailures = 2
	facts := append([]agentmodem.Fact(nil), runtime.facts...)
	runtime.mu.Unlock()

	manager.ReconcilePolicies(context.Background(), facts)
	view := manager.View(policy.EquipmentID, policy.CardID)
	if view.State != "recovering" || view.Code != "profile_apply_failed" || runtime.saves != 1 {
		t.Fatalf("first failure view=%+v saves=%d", view, runtime.saves)
	}
	manager.ReconcilePolicies(context.Background(), facts)
	if runtime.saves != 1 || manager.View(policy.EquipmentID, policy.CardID).State != "recovering" {
		t.Fatalf("backoff was bypassed saves=%d view=%+v", runtime.saves, manager.View(policy.EquipmentID, policy.CardID))
	}
	now = now.Add(2 * time.Millisecond)
	manager.ReconcilePolicies(context.Background(), facts)
	view = manager.View(policy.EquipmentID, policy.CardID)
	if view.State != "recovering" || view.Code != "profile_apply_failed" || runtime.saves != 2 {
		t.Fatalf("second failure view=%+v saves=%d", view, runtime.saves)
	}
	now = now.Add(3 * time.Millisecond)
	manager.ReconcilePolicies(context.Background(), facts)
	view = manager.View(policy.EquipmentID, policy.CardID)
	if view.State != "ready" || view.Code != "policy_ready" || runtime.saves != 3 {
		t.Fatalf("successful retry view=%+v saves=%d", view, runtime.saves)
	}
	manager.ReconcilePolicies(context.Background(), facts)
	if runtime.saves != 3 {
		t.Fatalf("confirmed profile was reapplied: saves=%d", runtime.saves)
	}
}

func TestPrepareLinearizesAgainstPolicyMutationsAndStopReopensThem(t *testing.T) {
	for _, test := range []struct {
		name    string
		request func(uint64) agentlink.ModemPolicyRequest
		verify  func(*testing.T, *testRuntime)
	}{
		{
			name: "data-off",
			request: func(revision uint64) agentlink.ModemPolicyRequest {
				return policyRequest(revision, agentlink.ModemPolicyPatch{CellularEnabled: boolPointer(false)})
			},
			verify: func(t *testing.T, runtime *testRuntime) {
				t.Helper()
				if runtime.radioCalls != 0 || runtime.saves != 0 {
					t.Fatalf("data-off hardware side effects radio=%d saves=%d", runtime.radioCalls, runtime.saves)
				}
			},
		},
		{
			name: "flight-on",
			request: func(revision uint64) agentlink.ModemPolicyRequest {
				return policyRequest(revision, agentlink.ModemPolicyPatch{FlightMode: boolPointer(true)})
			},
			verify: func(t *testing.T, runtime *testRuntime) {
				t.Helper()
				if runtime.radioCalls != 1 || runtime.saves != 0 {
					t.Fatalf("flight hardware side effects radio=%d saves=%d", runtime.radioCalls, runtime.saves)
				}
			},
		},
		{
			name: "profile-switch",
			request: func(revision uint64) agentlink.ModemPolicyRequest {
				request := policyRequest(revision, agentlink.ModemPolicyPatch{})
				request.Action = agentlink.ModemPolicyProfileSave
				request.Profile = agentlink.ModemProfileInput{
					Name: "profile-new", APN: "internet", Auth: "NONE", PasswordSet: true,
				}
				return request
			},
			verify: func(t *testing.T, runtime *testRuntime) {
				t.Helper()
				if runtime.radioCalls != 0 || runtime.saves != 1 {
					t.Fatalf("profile hardware side effects radio=%d saves=%d", runtime.radioCalls, runtime.saves)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "policy.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &testRuntime{facts: []agentmodem.Fact{{
				AttachmentID: "attachment-a", EquipmentID: "862547055201716",
				SIM:     agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "8985200000000000001", SessionGeneration: "session-a"},
				Network: agentmodem.NetworkFact{Registration: agentmodem.RegistrationHome, SoftwareRadio: agentmodem.RadioOn},
			}}}
			policy, err := New(Config{Store: store, Runtime: runtime, Coordinator: passthroughCoordinator{},
				Recovery: recovery.Policy{Base: time.Millisecond, Cap: time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = policy.Close() })
			persisted, _, err := store.Get("862547055201716", "8985200000000000001")
			if err != nil {
				t.Fatal(err)
			}
			persisted.Desired.CellularEnabled = true
			persisted, err = store.PutExpected(persisted, 0)
			if err != nil {
				t.Fatal(err)
			}
			backend := &blockingDataBackend{entered: make(chan struct{}), release: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			data, err := agentdata.NewManager(agentdata.Config{
				Context: ctx, ServerURL: "http://127.0.0.1:1", ServerToken: "0123456789abcdef0123456789abcdef",
				AgentID: "agent-a", ProcessGeneration: "process-a", HTTPClient: http.DefaultClient,
				Backend: backend, Coordinator: passthroughCoordinator{}, Admission: policy,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = data.Close() })
			if err := policy.BindCoordinator(data); err != nil {
				t.Fatal(err)
			}
			prepare := agentlink.ModemDataRequest{
				OperationID: "prepare-race", AttachmentID: "attachment-a", EquipmentID: persisted.EquipmentID,
				CardID: persisted.CardID, Action: agentlink.ModemDataPrepare, SessionID: "lease-race",
				Purpose: "egress:gb", ExpiresAt: time.Now().UTC().Add(time.Hour), MaxBytes: 1 << 20,
			}
			prepared := make(chan agentlink.ModemDataResponse, 1)
			go func() { prepared <- data.ExecuteModemData(context.Background(), prepare) }()
			select {
			case <-backend.entered:
			case <-time.After(time.Second):
				t.Fatal("data prepare did not enter the backend")
			}
			mutationRequest := test.request(persisted.Revision)
			mutated := make(chan agentlink.ModemPolicyResponse, 1)
			go func() { mutated <- policy.Execute(context.Background(), mutationRequest) }()
			select {
			case response := <-mutated:
				t.Fatalf("policy mutation escaped the data lifecycle lock: %+v", response)
			case <-time.After(20 * time.Millisecond):
			}
			close(backend.release)
			if response := <-prepared; response.Failure != nil {
				t.Fatalf("prepare=%+v", response)
			}
			blocked := <-mutated
			if blocked.Failure == nil || blocked.Failure.Code != "data_lease_active" {
				t.Fatalf("blocked mutation=%+v", blocked)
			}
			unchanged, found, err := store.Get(persisted.EquipmentID, persisted.CardID)
			if err != nil || !found || unchanged.Revision != persisted.Revision || unchanged.Desired != persisted.Desired {
				t.Fatalf("blocked mutation changed policy: %+v found=%t err=%v", unchanged, found, err)
			}
			if runtime.radioCalls != 0 || runtime.saves != 0 {
				t.Fatalf("blocked mutation reached hardware radio=%d saves=%d", runtime.radioCalls, runtime.saves)
			}
			stop := prepare
			stop.OperationID, stop.Action = "stop-race", agentlink.ModemDataStop
			stop.ExpiresAt, stop.MaxBytes = time.Time{}, 0
			if response := data.ExecuteModemData(context.Background(), stop); response.Failure != nil {
				t.Fatalf("stop=%+v", response)
			}
			applied := policy.Execute(context.Background(), mutationRequest)
			if applied.Failure != nil || applied.Policy == nil || applied.Policy.Revision != persisted.Revision+1 {
				t.Fatalf("post-stop mutation=%+v", applied)
			}
			test.verify(t, runtime)
			backend.mu.Lock()
			stops := backend.stops
			backend.mu.Unlock()
			if stops != 1 {
				t.Fatalf("backend stops=%d, want one exact cleanup", stops)
			}
		})
	}
}

func policyRequest(revision uint64, patch agentlink.ModemPolicyPatch) agentlink.ModemPolicyRequest {
	return agentlink.ModemPolicyRequest{
		OperationID: "policy-race", AttachmentID: "attachment-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", SIMSessionGeneration: "session-a",
		Action: agentlink.ModemPolicySet, ExpectedRevision: revision, Patch: patch,
	}
}

func boolPointer(value bool) *bool { return &value }
