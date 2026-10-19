package agentpolicy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentconnection"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

// Only hardware is synthetic. The durable policy, bearer sharing, borrower
// expiry and paid-call gate use their production managers and stores.
type sharedHardware struct {
	*testRuntime
	forced      bool
	healthErr   error
	stopErr     error
	starts      int
	stops       int
	beforeProbe func()
}

func (hardware *sharedHardware) Probe(ctx context.Context) ([]agentmodem.Fact, error) {
	hardware.mu.Lock()
	before := hardware.beforeProbe
	hardware.beforeProbe = nil
	hardware.mu.Unlock()
	if before != nil {
		before()
	}
	return hardware.testRuntime.Probe(ctx)
}

func (hardware *sharedHardware) ConnectionNeedsRecovery(context.Context, Target) (bool, error) {
	hardware.mu.Lock()
	defer hardware.mu.Unlock()
	return hardware.forced, hardware.healthErr
}

func (hardware *sharedHardware) PrepareData(_ context.Context, _ agentdata.Target, profile agentdata.Profile) (string, error) {
	hardware.mu.Lock()
	defer hardware.mu.Unlock()
	hardware.starts++
	hardware.facts[0].Network.Data = agentmodem.DataConnected
	return profile.Name, nil
}

func (hardware *sharedHardware) StopData(context.Context, agentdata.Target) error {
	hardware.mu.Lock()
	defer hardware.mu.Unlock()
	hardware.stops++
	if hardware.stopErr != nil {
		return hardware.stopErr
	}
	hardware.forced = false
	hardware.facts[0].Network.Data = agentmodem.DataDisconnected
	hardware.facts[0].AT.State = agentmodem.ATControlReady
	return nil
}

func (*sharedHardware) DialData(context.Context, agentdata.Target, string, string) (net.Conn, error) {
	return nil, errors.New("unexpected hardware dial")
}

func (*sharedHardware) Operate(context.Context, agentmodem.Operation) (agentmodem.OperationResult, error) {
	return agentmodem.OperationResult{}, errors.New("unexpected paid operation")
}

type sharedGate struct {
	data *agentdata.Manager
	call *agentcall.Manager
}

func (gate sharedGate) DoAuxiliary(ctx context.Context, equipment string, operation func(context.Context) error) error {
	return gate.data.DoAuxiliary(ctx, equipment, func(ctx context.Context) error {
		return gate.call.DoAuxiliary(ctx, equipment, operation)
	})
}

type sharedFixture struct {
	policy     *Manager
	hardware   *sharedHardware
	connection *agentconnection.Manager
	data       *agentdata.Manager
	calls      *agentcall.Store
	target     agentdata.Target
	stored     Policy
	now        time.Time
}

func newSharedFixture(t *testing.T) *sharedFixture {
	t.Helper()
	policy, runtime, _ := testManager(t)
	hardware := &sharedHardware{testRuntime: runtime}
	policy.config.Runtime = hardware
	fact := &hardware.facts[0]
	fact.AT.State = agentmodem.ATControlReady
	fact.Network.Data = agentmodem.DataDisconnected
	fact.Network.Guard.State = agentmodem.DataGuardProtected
	fact.Network.Registration = agentmodem.RegistrationRoaming
	stored, err := policy.config.Store.SaveProfileExpected(fact.EquipmentID, fact.SIM.ICCID,
		Profile{Name: "fixture", APN: "internet", Auth: "PAP", Username: "fixture-user", Password: "fixture-secret"}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	stored.Desired.CellularEnabled, stored.Desired.ConnectionEnabled, stored.Desired.RoamingEnabled = true, true, true
	stored, err = policy.config.Store.PutExpected(stored, stored.Revision)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := agentconnection.New(hardware)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.BindConnection(connection); err != nil {
		t.Fatal(err)
	}
	calls, err := agentcall.Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = calls.Close() })
	call, err := agentcall.NewManager(calls, hardware)
	if err != nil {
		t.Fatal(err)
	}
	data, err := agentdata.NewManager(agentdata.Config{Context: context.Background(),
		ServerURL: "http://127.0.0.1:1", ServerToken: "0123456789abcdef0123456789abcdef",
		AgentID: "fixture-agent", ProcessGeneration: "fixture-process", HTTPClient: http.DefaultClient,
		Backend: connection, Admission: policy, Coordinator: call})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := policy.BindCoordinator(sharedGate{data: data, call: call}); err != nil {
		t.Fatal(err)
	}
	fixture := &sharedFixture{policy: policy, hardware: hardware, connection: connection, data: data, calls: calls,
		stored: stored, now: time.Now(), target: agentdata.Target{AttachmentID: fact.AttachmentID,
			EquipmentID: fact.EquipmentID, CardID: fact.SIM.ICCID, SIMSessionGeneration: fact.SIM.SessionGeneration}}
	policy.config.Now = func() time.Time { return fixture.now }
	fixture.reconcile(t)
	if !connection.Persistent(fixture.target) {
		t.Fatal("initial persistent connection was not established")
	}
	return fixture
}

func (fixture *sharedFixture) reconcile(t *testing.T) {
	t.Helper()
	facts, err := fixture.hardware.testRuntime.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(time.Hour)
	fixture.policy.ReconcilePolicies(context.Background(), facts)
}

func (fixture *sharedFixture) borrow(t *testing.T, expires time.Time) agentlink.ModemDataRequest {
	t.Helper()
	request := agentlink.ModemDataRequest{OperationID: "fixture-prepare", AttachmentID: fixture.target.AttachmentID,
		EquipmentID: fixture.target.EquipmentID, CardID: fixture.target.CardID, SIMSessionGeneration: fixture.target.SIMSessionGeneration,
		Action: agentlink.ModemDataPrepare, SessionID: "fixture-borrower", Purpose: "egress:gb", ExpiresAt: expires, MaxBytes: 1 << 20}
	if response := fixture.data.ExecuteModemData(context.Background(), request); response.Failure != nil {
		t.Fatalf("borrow failed: %+v", response.Failure)
	}
	return request
}

func (fixture *sharedFixture) requireUnchanged(t *testing.T) {
	t.Helper()
	after, found, err := fixture.policy.config.Store.Get(fixture.target.EquipmentID, fixture.target.CardID)
	if err != nil || !found || after != fixture.stored {
		t.Fatal("recovery changed durable user intent")
	}
}

func TestSharedConnectionHealthyBorrowerConvergesWithoutMutation(t *testing.T) {
	fixture := newSharedFixture(t)
	request := fixture.borrow(t, time.Now().Add(time.Minute))
	fixture.reconcile(t)
	view := fixture.policy.View(fixture.target.EquipmentID, fixture.target.CardID)
	if view.State != "ready" || view.Code != "policy_ready" || !view.ConnectionActive || view.DataLease == nil || view.DataLease.State != "active" {
		t.Fatalf("healthy shared bearer did not converge: %+v", view)
	}
	request.Action, request.OperationID = agentlink.ModemDataRenew, "fixture-renew"
	request.ExpiresAt = request.ExpiresAt.Add(time.Second)
	if response := fixture.data.ExecuteModemData(context.Background(), request); response.Failure != nil {
		t.Fatalf("healthy renewal failed: %+v", response.Failure)
	}
	fixture.hardware.mu.Lock()
	starts, stops, radio, profiles := fixture.hardware.starts, fixture.hardware.stops, fixture.hardware.radioCalls, fixture.hardware.saves
	fixture.hardware.mu.Unlock()
	if starts != 1 || stops != 0 || radio != 0 || profiles != 1 {
		t.Fatalf("observation mutated hardware: starts=%d stops=%d radio=%d profiles=%d", starts, stops, radio, profiles)
	}
	fixture.requireUnchanged(t)
}

func TestSharedConnectionForcedCloseExpiresAndCannotReborrowBeforeRecovery(t *testing.T) {
	fixture := newSharedFixture(t)
	request := fixture.borrow(t, time.Now().Add(2*time.Second))
	fixture.hardware.mu.Lock()
	fixture.hardware.forced = true
	fixture.hardware.facts[0].AT.State = agentmodem.ATControlUnavailable
	fixture.hardware.mu.Unlock()
	renew := request
	renew.Action, renew.OperationID, renew.ExpiresAt = agentlink.ModemDataRenew, "fixture-renew", time.Now().Add(time.Minute)
	response := fixture.data.ExecuteModemData(context.Background(), renew)
	if response.Failure == nil || response.Failure.Code != "cellular_control_recovery_required" {
		t.Fatalf("forced-closed borrower was renewed: %+v", response)
	}
	open := request
	open.Action, open.OperationID, open.StreamID = agentlink.ModemDataOpen, "fixture-open", "fixture-stream"
	open.StreamToken, open.Network, open.Address = "0123456789abcdef0123456789abcdef", "tcp", "127.0.0.1:1"
	if response := fixture.data.ExecuteModemData(context.Background(), open); response.Failure == nil || response.Failure.Code != "cellular_control_recovery_required" {
		t.Fatalf("forced-closed stream admitted: %+v", response)
	}
	fixture.reconcile(t)
	if view := fixture.policy.View(fixture.target.EquipmentID, fixture.target.CardID); view.Code != "data_lease_active" {
		t.Fatalf("busy recovery lost typed gate: %+v", view)
	}
	// Wait for the real original expiry watcher, not a test deletion of its owner.
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for fixture.policy.DataLeaseActive(fixture.target.EquipmentID) {
		select {
		case <-deadline.C:
			t.Fatal("rejected renew extended the original lease")
		case <-tick.C:
		}
	}
	if !fixture.connection.Persistent(fixture.target) || fixture.hardware.stops != 0 {
		t.Fatal("borrower expiry bypassed persistent ownership")
	}
	request.OperationID, request.SessionID, request.ExpiresAt = "fixture-reborrow", "fixture-next", time.Now().Add(time.Minute)
	if response := fixture.data.ExecuteModemData(context.Background(), request); response.Failure == nil || response.Failure.Code != "cellular_control_recovery_required" {
		t.Fatalf("new borrower stole recovery window: %+v", response)
	}
	if _, _, err := fixture.calls.Begin(agentcall.Record{SchemaVersion: 1, LeaseID: "fixture-call", OperationID: "fixture-call-op",
		AttachmentID: fixture.target.AttachmentID, EquipmentID: fixture.target.EquipmentID, CardID: fixture.target.CardID,
		Direction: "out", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	fixture.reconcile(t)
	if fixture.hardware.stops != 0 || !fixture.connection.Persistent(fixture.target) {
		t.Fatal("recovery bypassed the real paid-call gate")
	}
	if err := fixture.calls.ClearTarget(fixture.target.AttachmentID, fixture.target.EquipmentID, fixture.target.CardID); err != nil {
		t.Fatal(err)
	}
	fixture.hardware.stopErr = errors.New("cleanup not confirmed")
	fixture.reconcile(t)
	if !fixture.connection.Persistent(fixture.target) || fixture.hardware.starts != 1 {
		t.Fatal("unknown cleanup discarded owner or created replacement")
	}
	fixture.hardware.stopErr = nil
	fixture.reconcile(t)
	fixture.reconcile(t)
	if view := fixture.policy.View(fixture.target.EquipmentID, fixture.target.CardID); view.State != "ready" || fixture.hardware.starts != 2 {
		t.Fatalf("original cleanup did not restore desired connection: %+v", view)
	}
	if response := fixture.data.ExecuteModemData(context.Background(), request); response.Failure != nil {
		t.Fatalf("recovered bearer could not be borrowed: %+v", response)
	}
	fixture.requireUnchanged(t)
}

func TestSharedConnectionReadyRequiresFreshIdentityProfileAndRevision(t *testing.T) {
	for _, change := range []string{"radio", "guard", "unknown-bearer", "session", "attachment", "roaming", "profile-secret", "flight", "revision-during-observation", "cleanup", "unknown-health"} {
		t.Run(change, func(t *testing.T) {
			fixture := newSharedFixture(t)
			fixture.borrow(t, time.Now().Add(time.Minute))
			fixture.reconcile(t)
			if view := fixture.policy.View(fixture.target.EquipmentID, fixture.target.CardID); view.State != "ready" {
				t.Fatalf("healthy prerequisite did not converge: %+v", view)
			}
			mutate := func() {
				policy := fixture.stored
				policy.Desired.RoamingEnabled = false
				if _, err := fixture.policy.config.Store.PutExpected(policy, policy.Revision); err != nil {
					t.Fatal(err)
				}
			}
			switch change {
			case "radio":
				fixture.hardware.facts[0].Network.SoftwareRadio = agentmodem.RadioOff
			case "guard":
				fixture.hardware.facts[0].Network.Guard.State = agentmodem.DataGuardFailed
			case "unknown-bearer":
				fixture.hardware.facts[0].Network.Data = agentmodem.DataUnknown
				if err := fixture.policy.ValidateDataTarget(context.Background(), fixture.target); err == nil {
					t.Fatal("unknown retained bearer admitted data")
				}
			case "session":
				fixture.hardware.facts[0].SIM.SessionGeneration = "replacement-session"
			case "attachment":
				fixture.hardware.facts[0].AttachmentID = "replacement-attachment"
			case "roaming":
				mutate()
			case "profile-secret":
				if _, err := fixture.policy.config.Store.SaveProfileExpected(fixture.target.EquipmentID, fixture.target.CardID,
					Profile{Name: "fixture", APN: "internet", Auth: "PAP", Username: "fixture-user", Password: "changed-secret"}, false, fixture.stored.Revision); err != nil {
					t.Fatal(err)
				}
			case "flight":
				policy := fixture.stored
				policy.Desired.FlightMode = true
				if _, err := fixture.policy.config.Store.PutExpected(policy, policy.Revision); err != nil {
					t.Fatal(err)
				}
			case "revision-during-observation":
				fixture.hardware.beforeProbe = mutate
			case "cleanup":
				fixture.policy.DataCleanup(fixture.target, "fixture-borrower", "egress:gb")
			case "unknown-health":
				fixture.hardware.facts[0].AT.State = agentmodem.ATControlUnavailable
				fixture.hardware.healthErr = context.DeadlineExceeded
				if err := fixture.policy.ValidateDataTarget(context.Background(), fixture.target); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("unknown health changed into confirmed forced close: %v", err)
				}
			}
			fixture.reconcile(t)
			if view := fixture.policy.View(fixture.target.EquipmentID, fixture.target.CardID); view.State == "ready" {
				t.Fatalf("changed observation incorrectly ready: %+v", view)
			}
			if fixture.hardware.starts != 1 || fixture.hardware.stops != 0 || fixture.hardware.radioCalls != 0 || fixture.hardware.saves != 1 {
				t.Fatal("mismatch bypassed active borrower's mutation gate")
			}
		})
	}
}
