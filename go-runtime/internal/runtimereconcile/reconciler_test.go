package runtimereconcile

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type fakeAgents struct {
	mu       sync.Mutex
	statuses []agentlink.ConnectionStatus
	voiceErr error
	smsErr   error
	dataErr  error
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

func (agents *fakeAgents) ResolveModemTargetForCardAction(cardID string,
	action agentlink.ModemAction) (agentlink.ModemTarget, error) {
	agents.mu.Lock()
	err := agents.voiceErr
	if action == agentlink.ModemSMSList || action == agentlink.ModemSMSSend {
		err = agents.smsErr
	}
	agents.mu.Unlock()
	if err != nil {
		return agentlink.ModemTarget{}, err
	}
	return agents.resolveModem(cardID, action, false)
}

func (agents *fakeAgents) ResolveModemDataTargetForCard(cardID string) (agentlink.ModemTarget, error) {
	agents.mu.Lock()
	err := agents.dataErr
	agents.mu.Unlock()
	if err != nil {
		return agentlink.ModemTarget{}, err
	}
	return agents.resolveModem(cardID, "", true)
}

func (agents *fakeAgents) resolveModem(cardID string, action agentlink.ModemAction,
	data bool) (agentlink.ModemTarget, error) {
	statuses := agents.Statuses()
	occurrences := 0
	for _, status := range statuses {
		if status.Topology == nil {
			continue
		}
		for _, reader := range status.Topology.Readers {
			if reader.CardPresent && reader.IdentityState == agentlink.CardIdentified && reader.CardID == cardID {
				occurrences++
			}
		}
		for _, modem := range status.Topology.Modems {
			if modem.SIM.State == "ready" && modem.SIM.SessionGeneration != "" && modem.SIM.ICCID == cardID {
				occurrences++
			}
		}
	}
	if occurrences == 0 {
		return agentlink.ModemTarget{}, agentlink.ErrModemOffline
	}
	if occurrences != 1 {
		return agentlink.ModemTarget{}, agentlink.ErrModemAmbiguous
	}
	matches := []agentlink.ModemTarget{}
	for _, status := range statuses {
		if status.Topology == nil || status.Topology.ModemCondition != agentlink.ModemReady {
			continue
		}
		for _, modem := range status.Topology.Modems {
			capable := modem.AT.State == "ready" && modem.SIM.State == "ready" && modem.SIM.ICCID == cardID
			if data {
				capable = capable && modem.Capabilities.CellularData && modem.Network.DataGuard == "protected"
			} else if action == agentlink.ModemSMSList || action == agentlink.ModemSMSSend {
				capable = capable && modem.AT.SMS
			} else {
				capable = capable && modem.AT.CallSignalling
			}
			if capable {
				matches = append(matches, agentlink.ModemTarget{
					AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
					AttachmentID: modem.AttachmentID, EquipmentID: modem.EquipmentID, CardID: cardID,
				})
			}
		}
	}
	if len(matches) == 0 {
		return agentlink.ModemTarget{}, agentlink.ErrModemOffline
	}
	if len(matches) != 1 {
		return agentlink.ModemTarget{}, agentlink.ErrModemAmbiguous
	}
	return matches[0], nil
}

type fakeRuntime struct {
	mu           sync.Mutex
	status       vowifiipc.Snapshot
	fence        mediaauth.ProviderFence
	starts       []string
	stops        []string
	stopRequests []vowifiipc.LifecycleRequest
	startErr     error
	stopErr      error
	actions      chan string
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

func (runtime *fakeRuntime) Observe(context.Context, string) (vowifiipc.Snapshot, mediaauth.ProviderFence, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.status, runtime.fence, nil
}

func (runtime *fakeRuntime) Start(_ context.Context, _ string, _ mediaauth.ProviderFence, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
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

func (runtime *fakeRuntime) Stop(_ context.Context, _ string, _ mediaauth.ProviderFence, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	runtime.mu.Lock()
	runtime.stops = append(runtime.stops, request.OperationID)
	runtime.stopRequests = append(runtime.stopRequests, request)
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

func TestAgentFactsDoNotUsePresentationIMEIAsPhysicalRoute(t *testing.T) {
	statuses := []agentlink.ConnectionStatus{{
		AgentID: "agent-1", ProcessGeneration: "agent-generation-1", LastReport: time.Now(),
		Topology: &agentlink.TopologySnapshot{ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
			AttachmentID: "attachment-1", EquipmentID: "862547055201716", Condition: "ready",
			AT:  agentlink.ModemATControlFact{State: "ready", CallSignalling: true},
			SIM: agentlink.ModemSIMFact{State: "ready", SessionGeneration: "sim-session-1", ICCID: "8944100000000000001"},
		}}},
	}}
	reconciler, catalog, _, _, replay, _ := testReconciler(t, vowifiipc.RuntimeStopped, statuses)
	line, err := catalog.Get("line-1")
	if err != nil {
		t.Fatal(err)
	}
	line.SIM.IMEI = "867530900000099"
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	facts := projectionFacts(t, replay, "line-1")
	if !facts[state.LayerHardware].Available || !facts[state.LayerCard].Available ||
		!facts[state.LayerCellularVoice].Available {
		t.Fatalf("presentation IMEI blocked physical route: %+v", facts)
	}
}

func TestAgentFactsUseFinalRouteAdmissionForEveryCellularCapability(t *testing.T) {
	statuses := []agentlink.ConnectionStatus{{
		AgentID: "agent-1", ProcessGeneration: "agent-generation-1", LastReport: time.Now(),
		Topology: &agentlink.TopologySnapshot{ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
			AttachmentID: "attachment-1", EquipmentID: "862547055201716", Condition: "ready",
			Capabilities: agentlink.ModemCapabilities{CellularData: true},
			AT:           agentlink.ModemATControlFact{State: "ready", CallSignalling: true, SMS: true},
			SIM:          agentlink.ModemSIMFact{State: "ready", SessionGeneration: "sim-session-1", ICCID: "8944100000000000001"},
			Network:      agentlink.ModemNetworkFact{DataGuard: "protected"},
		}}},
	}}
	reconciler, _, _, agents, replay, _ := testReconciler(t, vowifiipc.RuntimeStopped, statuses)
	agents.mu.Lock()
	agents.voiceErr = agentlink.ErrModemOffline
	agents.smsErr = agentlink.ErrModemOffline
	agents.dataErr = agentlink.ErrModemOffline
	agents.mu.Unlock()
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	facts := projectionFacts(t, replay, "line-1")
	if !facts[state.LayerHardware].Available || !facts[state.LayerCard].Available {
		t.Fatalf("physical presence was lost: %+v", facts)
	}
	for _, layer := range []state.Layer{state.LayerCellularVoice, state.LayerCellularSMS, state.LayerCellularData} {
		if facts[layer].Available {
			t.Fatalf("final admission failure retained %s ready: %+v", layer, facts[layer])
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

func TestAgentFactsFailClosedAfterOneDuplicateReconcile(t *testing.T) {
	statuses := []agentlink.ConnectionStatus{{
		AgentID: "agent-1", ProcessGeneration: "agent-generation-1", LastReport: time.Now(),
		Topology: &agentlink.TopologySnapshot{ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
			AttachmentID: "attachment-1", EquipmentID: "862547055201716", Condition: "ready",
			AT:  agentlink.ModemATControlFact{State: "ready", CallSignalling: true},
			SIM: agentlink.ModemSIMFact{State: "ready", SessionGeneration: "sim-session-1", ICCID: "8944100000000000001"},
		}}},
	}}
	reconciler, _, _, agents, replay, _ := testReconciler(t, vowifiipc.RuntimeStopped, statuses)
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	duplicate := statuses[0]
	duplicate.AgentID = "agent-2"
	duplicate.ProcessGeneration = "agent-generation-2"
	topology := *statuses[0].Topology
	topology.Modems = append([]agentlink.ModemFact(nil), statuses[0].Topology.Modems...)
	duplicate.Topology = &topology
	duplicate.Topology.Modems[0].AttachmentID = "attachment-2"
	agents.set([]agentlink.ConnectionStatus{statuses[0], duplicate})
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	facts := projectionFacts(t, replay, "line-1")
	for _, layer := range []state.Layer{state.LayerAgentLink, state.LayerHardware, state.LayerCard,
		state.LayerCellularVoice, state.LayerCellularSMS, state.LayerCellularData} {
		if facts[layer].Available {
			t.Fatalf("duplicate retained %s ready after one reconcile: %+v", layer, facts[layer])
		}
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
	if len(runtime.stopRequests) != 1 || runtime.stopRequests[0].RequireIdle {
		t.Fatalf("convergence stop requests=%+v", runtime.stopRequests)
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

func TestWrongOrEmptyProviderCardCannotAdoptOrStart(t *testing.T) {
	for _, cardID := range []string{"", "8944100000000000999"} {
		t.Run(cardID, func(t *testing.T) {
			reconciler, catalog, runtime, _, replay, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
			runtime.mu.Lock()
			runtime.fence.CardID = cardID
			runtime.mu.Unlock()
			if err := reconciler.reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, found, _, err := catalog.RuntimeIntent("line-1"); err != nil || found {
				t.Fatalf("wrong-card route adopted intent found=%v err=%v", found, err)
			}
			select {
			case action := <-runtime.actions:
				t.Fatalf("wrong-card stopped route performed %s", action)
			default:
			}
			if fact := projectionFacts(t, replay, "line-1")[state.LayerAdmission]; fact.Available || fact.Code != "runtime_intent_uninitialized" {
				t.Fatalf("admission fact=%+v", fact)
			}
		})
	}
}

func TestRunningWrongCardProviderIsStoppedThroughItsExactFence(t *testing.T) {
	reconciler, _, runtime, _, _, _ := testReconciler(t, vowifiipc.RuntimeRunning, oneCard())
	runtime.mu.Lock()
	runtime.fence.CardID = "8944100000000000999"
	runtime.mu.Unlock()
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitAction(t, runtime.actions, "stop")
}

func TestStartPlanRechecksAgentCardBeforeProviderAction(t *testing.T) {
	reconciler, catalog, runtime, agents, _, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
		t.Fatal(err)
	}
	line, err := catalog.Get("line-1")
	if err != nil {
		t.Fatal(err)
	}
	intent, found, _, epoch, err := reconciler.readIntent("line-1")
	if err != nil {
		t.Fatal(err)
	}
	plan := reconciler.actionPlan(line, lineObservation{
		intentEnabled: intent, intentFound: found, intentEpoch: epoch,
		cardMatches: 1, providerReady: true, fence: runtime.fence, status: runtime.status,
	}, "start", false)
	reconciler.mu.Lock()
	state := reconciler.lineLocked(line.ID)
	state.action, state.actionKey, state.operationID, state.inFlight = "start", plan.key(), "start-recheck", true
	reconciler.mu.Unlock()
	agents.set(nil)
	reconciler.wg.Add(1)
	reconciler.execute(plan, "start-recheck")
	select {
	case action := <-runtime.actions:
		t.Fatalf("changed Agent card reached Provider with %s", action)
	default:
	}
}

func TestConvergencePlanRechecksRuntimeBeforeProviderAction(t *testing.T) {
	tests := []struct {
		name             string
		action           string
		intent           bool
		observedRuntime  vowifiipc.RuntimeCondition
		completedRuntime vowifiipc.RuntimeCondition
	}{
		{
			name: "completed_start_is_not_repeated", action: "start", intent: true,
			observedRuntime: vowifiipc.RuntimeStopped, completedRuntime: vowifiipc.RuntimeRunning,
		},
		{
			name: "completed_stop_is_not_repeated", action: "stop", intent: false,
			observedRuntime: vowifiipc.RuntimeRunning, completedRuntime: vowifiipc.RuntimeStopped,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reconciler, catalog, runtime, _, _, _ := testReconciler(t, test.observedRuntime, oneCard())
			if _, _, _, err := catalog.SetRuntimeIntent("line-1", test.intent); err != nil {
				t.Fatal(err)
			}
			line, err := catalog.Get("line-1")
			if err != nil {
				t.Fatal(err)
			}
			intent, found, _, epoch, err := reconciler.readIntent(line.ID)
			if err != nil {
				t.Fatal(err)
			}
			plan := reconciler.actionPlan(line, lineObservation{
				intentEnabled: intent, intentFound: found, intentEpoch: epoch,
				cardMatches: 1, providerReady: true, fence: runtime.fence, status: runtime.status,
			}, test.action, false)
			reconciler.mu.Lock()
			state := reconciler.lineLocked(line.ID)
			state.action, state.actionKey, state.operationID, state.inFlight =
				test.action, plan.key(), "stale-convergence", true
			reconciler.mu.Unlock()
			runtime.mu.Lock()
			runtime.status.Runtime.Condition = test.completedRuntime
			runtime.mu.Unlock()

			reconciler.wg.Add(1)
			reconciler.execute(plan, "stale-convergence")
			select {
			case action := <-runtime.actions:
				t.Fatalf("stale convergence reached Provider with %s", action)
			default:
			}
		})
	}
}

func TestRecoveryStopReobservesActiveCallAndHealthyStateClearsEpisode(t *testing.T) {
	reconciler, catalog, runtime, _, _, clock := testReconciler(t, vowifiipc.RuntimeFailed, oneCard())
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
		t.Fatal(err)
	}
	line, err := catalog.Get("line-1")
	if err != nil {
		t.Fatal(err)
	}
	intent, found, _, epoch, err := reconciler.readIntent("line-1")
	if err != nil {
		t.Fatal(err)
	}
	reconciler.mu.Lock()
	state := reconciler.lineLocked(line.ID)
	state.recovering, state.recoveryEpisode = true, 1
	state.recoveryFailures, state.recoveryNext = 3, clock.Now().Add(time.Minute)
	reconciler.mu.Unlock()
	plan := reconciler.actionPlan(line, lineObservation{
		intentEnabled: intent, intentFound: found, intentEpoch: epoch,
		cardMatches: 1, providerReady: true, fence: runtime.fence, status: runtime.status,
	}, "stop", true)
	runtime.mu.Lock()
	runtime.status.ActiveCall = &vowifiipc.ActiveCall{CallID: "call-raced", Condition: vowifiipc.CallActive}
	runtime.mu.Unlock()
	reconciler.mu.Lock()
	state.action, state.actionKey, state.operationID, state.inFlight = "stop", plan.key(), "stop-recheck", true
	reconciler.mu.Unlock()
	reconciler.wg.Add(1)
	reconciler.execute(plan, "stop-recheck")
	select {
	case action := <-runtime.actions:
		t.Fatalf("recovery raced active call and reached Provider with %s", action)
	default:
	}
	runtime.mu.Lock()
	runtime.status.ActiveCall = nil
	runtime.status.Runtime.Condition = vowifiipc.RuntimeRunning
	runtime.status.Tunnel = vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	runtime.mu.Unlock()
	if err := reconciler.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	reconciler.mu.Lock()
	stillRecovering := state.recovering
	recoveryFailures, recoveryNext := state.recoveryFailures, state.recoveryNext
	reconciler.mu.Unlock()
	if stillRecovering || recoveryFailures != 3 || recoveryNext.IsZero() {
		t.Fatalf("healthy cleanup recovering=%v failures=%d next=%v", stillRecovering, recoveryFailures, recoveryNext)
	}
}

func TestUnknownLifecycleRetryReusesOperationIDAndTypedFailureDoesNot(t *testing.T) {
	tests := []struct {
		name string
		err  error
		same bool
	}{
		{name: "unknown_transport_outcome", err: context.DeadlineExceeded, same: true},
		{name: "typed_terminal_failure", err: &vowifiipc.ResponseError{
			Status:  http.StatusPreconditionFailed,
			Failure: vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "ims_unavailable", Layer: "ims"},
		}, same: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reconciler, catalog, runtime, _, _, clock := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
			if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
				t.Fatal(err)
			}
			runtime.startErr = test.err
			if err := reconciler.reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			waitAction(t, runtime.actions, "start")
			waitIdle(t, reconciler, "line-1")
			clock.Advance(5 * time.Second)
			if err := reconciler.reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			waitAction(t, runtime.actions, "start")
			waitIdle(t, reconciler, "line-1")
			runtime.mu.Lock()
			starts := append([]string(nil), runtime.starts...)
			runtime.mu.Unlock()
			if len(starts) != 2 || (starts[0] == starts[1]) != test.same {
				t.Fatalf("operation IDs=%v want same=%v", starts, test.same)
			}
		})
	}
}

func TestRequestIntentSupersessionAndDisabledFutureIntent(t *testing.T) {
	reconciler, catalog, runtime, _, _, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
	startResult := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() {
		_, err := reconciler.RequestIntent(ctx, "line-1", true, "public-start")
		startResult <- err
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if enabled, found, _, _ := catalog.RuntimeIntent("line-1"); found && enabled {
			break
		}
		time.Sleep(time.Millisecond)
	}
	result, err := reconciler.RequestIntent(ctx, "line-1", false, "public-stop")
	if err != nil || result.Code != "stopped" || result.Status.Runtime.Condition != vowifiipc.RuntimeStopped {
		t.Fatalf("stop result=%+v err=%v", result, err)
	}
	err = <-startResult
	var failure *vowifiipc.OperationError
	if !errors.As(err, &failure) || failure.Code != "runtime_intent_superseded" {
		t.Fatalf("superseded start err=%v", err)
	}

	line, err := catalog.Get("line-1")
	if err != nil {
		t.Fatal(err)
	}
	line.Enabled = false
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	_, err = reconciler.RequestIntent(ctx, "line-1", true, "disabled-start")
	failure = nil
	if !errors.As(err, &failure) || failure.Code != "line_disabled" {
		t.Fatalf("disabled start err=%v", err)
	}
	if enabled, found, _, intentErr := catalog.RuntimeIntent("line-1"); intentErr != nil || !found || !enabled {
		t.Fatalf("future intent enabled=%v found=%v err=%v", enabled, found, intentErr)
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("request facade called Provider directly with %s", action)
	default:
	}
}

func TestRequestIntentRejectsABAWhenDesiredReturnsToSameValue(t *testing.T) {
	reconciler, catalog, _, _, _, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := reconciler.RequestIntent(ctx, "line-1", true, "public-start-before-aba")
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		reconciler.mu.Lock()
		line := reconciler.lines["line-1"]
		ready := line != nil && line.intentKnown && line.intentFound && line.intentValue
		reconciler.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	reconciler.mu.Lock()
	line := reconciler.lineLocked("line-1")
	originalEpoch := line.intentEpoch
	_, changed, _, sameEpoch, err := reconciler.setIntentLocked("line-1", true)
	if err != nil || changed || sameEpoch != originalEpoch {
		reconciler.mu.Unlock()
		t.Fatalf("same desired changed=%v epoch=%d original=%d err=%v", changed, sameEpoch, originalEpoch, err)
	}
	_, changed, _, stopEpoch, err := reconciler.setIntentLocked("line-1", false)
	if err != nil || !changed || stopEpoch == originalEpoch {
		reconciler.mu.Unlock()
		t.Fatalf("stop transition changed=%v epoch=%d original=%d err=%v", changed, stopEpoch, originalEpoch, err)
	}
	_, changed, _, restartEpoch, err := reconciler.setIntentLocked("line-1", true)
	reconciler.mu.Unlock()
	if err != nil || !changed || restartEpoch == originalEpoch || restartEpoch == stopEpoch {
		t.Fatalf("restart transition changed=%v epoch=%d stop=%d original=%d err=%v", changed, restartEpoch, stopEpoch, originalEpoch, err)
	}
	if enabled, found, _, err := catalog.RuntimeIntent("line-1"); err != nil || !found || !enabled {
		t.Fatalf("final ABA intent enabled=%v found=%v err=%v", enabled, found, err)
	}
	var failure *vowifiipc.OperationError
	if err := <-result; !errors.As(err, &failure) || failure.Code != "runtime_intent_superseded" {
		t.Fatalf("ABA waiter err=%v", err)
	}
}

func TestRequestIntentConvergesThroughSingleReconcilerExecutor(t *testing.T) {
	reconciler, catalog, runtime, _, _, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
	if _, _, _, err := catalog.SetRuntimeIntent("line-1", false); err != nil {
		t.Fatal(err)
	}
	reconciler.Start()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	started, err := reconciler.RequestIntent(ctx, "line-1", true, "public-start-full-chain")
	if err != nil || started.Code != "started" || started.Status.Runtime.Condition != vowifiipc.RuntimeRunning {
		t.Fatalf("start result=%+v err=%v", started, err)
	}
	stopped, err := reconciler.RequestIntent(ctx, "line-1", false, "public-stop-full-chain")
	if err != nil || stopped.Code != "stopped" || stopped.Status.Runtime.Condition != vowifiipc.RuntimeStopped {
		t.Fatalf("stop result=%+v err=%v", stopped, err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.starts) != 1 || len(runtime.stops) != 1 {
		t.Fatalf("starts=%v stops=%v", runtime.starts, runtime.stops)
	}
}

func TestRequestIntentReturnsLineDisabledIfGateChangesWhileWaiting(t *testing.T) {
	reconciler, catalog, runtime, _, _, _ := testReconciler(t, vowifiipc.RuntimeStopped, oneCard())
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := reconciler.RequestIntent(ctx, "line-1", true, "start-before-disable")
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if enabled, found, _, _ := catalog.RuntimeIntent("line-1"); found && enabled {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if enabled, found, _, _ := catalog.RuntimeIntent("line-1"); !found || !enabled {
		t.Fatal("start request did not persist intent")
	}
	line, err := catalog.Get("line-1")
	if err != nil {
		t.Fatal(err)
	}
	line.Enabled = false
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	var failure *vowifiipc.OperationError
	if err := <-result; !errors.As(err, &failure) || failure.Code != "line_disabled" {
		t.Fatalf("disabled waiter err=%v", err)
	}
	if enabled, found, _, err := catalog.RuntimeIntent("line-1"); err != nil || !found || !enabled {
		t.Fatalf("future intent enabled=%v found=%v err=%v", enabled, found, err)
	}
	select {
	case action := <-runtime.actions:
		t.Fatalf("disabled waiter called Provider with %s", action)
	default:
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
	runtime.mu.Lock()
	if len(runtime.stopRequests) != 1 || !runtime.stopRequests[0].RequireIdle {
		runtime.mu.Unlock()
		t.Fatalf("recovery stop requests=%+v", runtime.stopRequests)
	}
	runtime.mu.Unlock()
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
	}, fence: mediaauth.ProviderFence{
		LineID: "line-1", ProviderID: "provider-1", Generation: "provider-generation-1",
		CardID: "8944100000000000001",
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
