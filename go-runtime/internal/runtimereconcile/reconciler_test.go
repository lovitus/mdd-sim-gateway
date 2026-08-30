package runtimereconcile

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type fakeAgents struct {
	mu       sync.Mutex
	statuses []agentlink.ConnectionStatus
}

func (agents *fakeAgents) Statuses() []agentlink.ConnectionStatus {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	return append([]agentlink.ConnectionStatus(nil), agents.statuses...)
}

func (agents *fakeAgents) set(statuses []agentlink.ConnectionStatus) {
	agents.mu.Lock()
	agents.statuses = statuses
	agents.mu.Unlock()
}

type fakeRuntime struct {
	mu       sync.Mutex
	status   vowifiipc.Snapshot
	starts   []string
	stops    []string
	startErr error
	stopErr  error
	actions  chan string
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func (runtime *fakeRuntime) Status(context.Context, string) (vowifiipc.Snapshot, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.status, nil
}

func (runtime *fakeRuntime) Start(_ context.Context, _ string, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	runtime.mu.Lock()
	runtime.starts = append(runtime.starts, request.OperationID)
	err := runtime.startErr
	if err == nil {
		runtime.status.Runtime.Condition = vowifiipc.RuntimeRunning
		runtime.status.Runtime.Code = "ready"
		ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
		runtime.status.Tunnel, runtime.status.IMS = ready, ready
		runtime.status.Voice, runtime.status.Messaging = ready, ready
	}
	runtime.mu.Unlock()
	runtime.actions <- "start"
	return vowifiipc.OperationResult{}, err
}

func (runtime *fakeRuntime) Stop(_ context.Context, _ string, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	runtime.mu.Lock()
	runtime.stops = append(runtime.stops, request.OperationID)
	err := runtime.stopErr
	if err == nil {
		runtime.status.Runtime.Condition = vowifiipc.RuntimeStopped
		runtime.status.Runtime.Code = "stopped"
		stopped := vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped, Code: "stopped"}
		runtime.status.Tunnel, runtime.status.IMS = stopped, stopped
		runtime.status.Voice, runtime.status.Messaging = stopped, stopped
	}
	runtime.mu.Unlock()
	runtime.actions <- "stop"
	return vowifiipc.OperationResult{}, err
}

func TestFirstObservationAdoptsRunningProviderWithoutLifecycleAction(t *testing.T) {
	reconciler, catalog, runtime, _, replay, _ := testReconciler(t, vowifiipc.RuntimeRunning, oneCard())
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	enabled, found, _, err := catalog.RuntimeIntent("line-1")
	if err != nil || !found || !enabled {
		t.Fatalf("adopted intent enabled=%v found=%v err=%v", enabled, found, err)
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("adoption performed %s", action)
	default:
	}
	facts := projectionFacts(t, replay, "line-1")
	for _, layer := range []state.Layer{state.LayerIntent, state.LayerVoWiFiIntent, state.LayerCardRoute, state.LayerEngineProcess, state.LayerAdmission} {
		if fact := facts[layer]; !fact.Fresh || !fact.Available || fact.Condition != state.ConditionReady {
			t.Fatalf("layer %s fact=%+v", layer, fact)
		}
	}
}

func TestAgentModemFactsFollowExactCurrentCardAndCapabilities(t *testing.T) {
	statuses := []agentlink.ConnectionStatus{{
		AgentID: "agent-1", ProcessGeneration: "agent-generation-1", LastReport: time.Now(),
		Topology: &agentlink.TopologySnapshot{ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
			AttachmentID: "attachment-1", EquipmentID: "862547055201716", Condition: "ready",
			Capabilities: agentlink.ModemCapabilities{CellularData: true, SMSSend: true},
			AT:           agentlink.ModemATControlFact{State: "ready", CallSignalling: true, SMS: true},
			SIM:          agentlink.ModemSIMFact{State: "ready", SessionGeneration: "sim-session-1", ICCID: "8944100000000000001", PINState: "not_required"},
			Network:      agentlink.ModemNetworkFact{DataGuard: "protected"},
		}}},
	}}
	reconciler, _, _, _, replay, _ := testReconciler(t, vowifiipc.RuntimeStopped, statuses)
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	facts := projectionFacts(t, replay, "line-1")
	for _, layer := range []state.Layer{state.LayerAgentLink, state.LayerHardware, state.LayerCard, state.LayerPIN,
		state.LayerCellularData, state.LayerCellularVoice, state.LayerCellularSMS} {
		if fact := facts[layer]; !fact.Fresh || !fact.Available || fact.Condition != state.ConditionReady {
			t.Fatalf("layer %s fact=%+v", layer, fact)
		}
	}
}

func TestAgentFactsClearWhenCardIsRemoved(t *testing.T) {
	reconciler, _, _, agents, replay, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	agents.set(nil)
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	facts := projectionFacts(t, replay, "line-1")
	if facts[state.LayerAgentLink].Available || facts[state.LayerCard].Available || facts[state.LayerCellularVoice].Available {
		t.Fatalf("removed card retained ready Agent facts: %+v", facts)
	}
}

func TestFirstObservationPreservesStoppedProviderAsDisabledIntent(t *testing.T) {
	reconciler, catalog, runtime, _, replay, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	enabled, found, _, err := catalog.RuntimeIntent("line-1")
	if err != nil || !found || enabled {
		t.Fatalf("adopted intent enabled=%v found=%v err=%v", enabled, found, err)
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("stopped adoption performed %s", action)
	default:
	}
	facts := projectionFacts(t, replay, "line-1")
	if fact := facts[state.LayerIntent]; !fact.Available || fact.Code != "line_enabled" {
		t.Fatalf("global intent fact=%+v", fact)
	}
	if fact := facts[state.LayerVoWiFiIntent]; fact.Available || fact.Code != "vowifi_disabled" {
		t.Fatalf("VoWiFi intent fact=%+v", fact)
	}
}

func TestIntentAndExactCardConvergeRuntimeAcrossHotplug(t *testing.T) {
	reconciler, catalog, runtime, agents, _, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitAction(t, runtime.actions, "start")
	waitIdle(t, reconciler, "line-1")

	agents.set(nil)
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitAction(t, runtime.actions, "stop")
	waitIdle(t, reconciler, "line-1")
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.starts) != 1 || len(runtime.stops) != 1 || runtime.starts[0] == runtime.stops[0] ||
		runtime.starts[0] == "reconcile-start-1" {
		t.Fatalf("starts=%v stops=%v", runtime.starts, runtime.stops)
	}
}

func TestDuplicateCardBlocksStartInsteadOfGuessingOwner(t *testing.T) {
	statuses := append(oneCard(), oneCard()...)
	statuses[1].AgentID = "agent-2"
	statuses[1].ProcessGeneration = "agent-generation-2"
	statuses[1].Topology.Readers[0].ReaderName = "reader-2"
	statuses[1].Topology.Readers[0].SessionGeneration = "session-2"
	reconciler, catalog, runtime, _, replay, _ := testReconciler(t, vowifiipc.RuntimeStopped, statuses)
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("ambiguous card performed %s", action)
	case <-time.After(25 * time.Millisecond):
	}
	facts := projectionFacts(t, replay, "line-1")
	if fact := facts[state.LayerCardRoute]; fact.Available || fact.Code != "card_identity_ambiguous" {
		t.Fatalf("card route fact=%+v", fact)
	}
	if fact := facts[state.LayerAdmission]; fact.Available || fact.Code != "card_identity_ambiguous" {
		t.Fatalf("admission fact=%+v", fact)
	}
}

func TestStaleAgentTopologyCannotKeepRuntimeAdmitted(t *testing.T) {
	statuses := oneCard()
	statuses[0].LastReport = time.Now().Add(-agentTopologyTTL - time.Second)
	reconciler, catalog, runtime, _, replay, _ := testReconciler(t, vowifiipc.RuntimeStopped, statuses)
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("stale topology performed %s", action)
	case <-time.After(25 * time.Millisecond):
	}
	facts := projectionFacts(t, replay, "line-1")
	if fact := facts[state.LayerAdmission]; fact.Available || fact.Code != "card_not_present" {
		t.Fatalf("admission fact=%+v", fact)
	}
}

func TestRetryDelayHonorsProviderCooldownAndNeverExhausts(t *testing.T) {
	reconciler, _, _, _, _, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
	cooldown := 37 * time.Minute
	err := &vowifiipc.ResponseError{Failure: vowifiipc.OperationError{RetryAfter: cooldown}}
	if got := reconciler.retryDelay(99, err); got != cooldown {
		t.Fatalf("provider cooldown=%v want=%v", got, cooldown)
	}
	if got := reconciler.retryDelay(1, context.DeadlineExceeded); got != 5*time.Second {
		t.Fatalf("first backoff=%v", got)
	}
	if got := reconciler.retryDelay(99, context.DeadlineExceeded); got != 10*time.Minute {
		t.Fatalf("capped backoff=%v", got)
	}
}

func TestFailedRuntimeIsCleanedBeforeBackedOffRestart(t *testing.T) {
	reconciler, catalog, runtime, _, replay, clock := testReconciler(t, vowifiipc.RuntimeFailed, oneCard())
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitAction(t, runtime.actions, "stop")
	waitIdle(t, reconciler, "line-1")
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("recovery ignored backoff and performed %s", action)
	default:
	}
	if fact := projectionFacts(t, replay, "line-1")[state.LayerAdmission]; fact.Available || fact.Condition != state.ConditionBackoff || fact.Code != "runtime_recovery_backoff" {
		t.Fatalf("recovery admission fact=%+v", fact)
	}
	clock.Advance(5 * time.Second)
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitAction(t, runtime.actions, "start")
}

func TestTerminalTunnelFaultWaitsForActiveCallThenRecovers(t *testing.T) {
	reconciler, catalog, runtime, _, _, clock := testReconciler(t, vowifiipc.RuntimeRunning, oneCard())
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.status.Tunnel = vowifiipc.LayerStatus{Condition: vowifiipc.LayerDegraded, Code: "userspace_stack_failed"}
	runtime.status.ActiveCall = &vowifiipc.ActiveCall{CallID: "call-1", Condition: vowifiipc.CallActive}
	runtime.mu.Unlock()
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("active call fault recovery performed %s", action)
	default:
	}
	runtime.mu.Lock()
	runtime.status.ActiveCall = nil
	runtime.mu.Unlock()
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitAction(t, runtime.actions, "stop")
	waitIdle(t, reconciler, "line-1")
	clock.Advance(5 * time.Second)
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitAction(t, runtime.actions, "start")
}

func testReconciler(t *testing.T, condition vowifiipc.RuntimeCondition, statuses []agentlink.ConnectionStatus) (
	*Reconciler, *linecatalog.Store, *fakeRuntime, *fakeAgents, *events.Replay, *fakeClock,
) {
	t.Helper()
	root := t.TempDir()
	catalog, err := linecatalog.Open(filepath.Join(root, "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	line := linecatalog.Line{ID: "line-1", Enabled: true, CardID: "8944100000000000001", SIM: linecatalog.SIMConfig{
		IMSI: "234100000000001", MCC: "234", MNC: "10",
	}}
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	store, err := events.OpenBoltStore(filepath.Join(root, "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	replay, err := events.NewReplay(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Now()}
	runtime := &fakeRuntime{status: vowifiipc.Snapshot{
		LineID: "line-1", Runtime: vowifiipc.RuntimeStatus{Condition: condition},
	}, actions: make(chan string, 8)}
	agents := &fakeAgents{statuses: statuses}
	reconciler, err := New(Config{
		Context: t.Context(), Catalog: catalog, Agents: agents, Runtime: runtime,
		Store: store, Replay: replay, Interval: time.Second, ActionTimeout: time.Second,
		BaseBackoff: 5 * time.Second, MaxBackoff: 10 * time.Minute,
		Generation: "core-test-generation", Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reconciler.Close)
	return reconciler, catalog, runtime, agents, replay, clock
}

func oneCard() []agentlink.ConnectionStatus {
	return []agentlink.ConnectionStatus{{
		AgentID: "agent-1", ProcessGeneration: "agent-generation-1", LastReport: time.Now(),
		Topology: &agentlink.TopologySnapshot{
			ReaderCondition: agentlink.ReaderReady,
			Readers: []agentlink.ReaderFact{{
				ReaderName: "reader-1", CardPresent: true, SessionGeneration: "session-1",
				CardID: "8944100000000000001", IdentityState: agentlink.CardIdentified,
			}},
		},
	}}
}

func waitAction(t *testing.T, actions <-chan string, expected string) {
	t.Helper()
	select {
	case action := <-actions:
		if action != expected {
			t.Fatalf("action=%s want=%s", action, expected)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", expected)
	}
}

func waitIdle(t *testing.T, reconciler *Reconciler, lineID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		reconciler.mu.Lock()
		line := reconciler.lines[lineID]
		idle := line == nil || !line.inFlight
		reconciler.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for reconciler action to finish")
}

func projectionFacts(t *testing.T, replay *events.Replay, lineID string) map[state.Layer]state.FactView {
	t.Helper()
	result := make(map[state.Layer]state.FactView)
	for _, projection := range replay.Projections(time.Now()) {
		if projection.LineID != lineID {
			continue
		}
		for _, fact := range projection.Facts {
			result[fact.Layer] = fact
		}
	}
	return result
}
