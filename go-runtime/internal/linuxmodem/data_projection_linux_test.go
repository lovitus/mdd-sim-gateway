//go:build linux

package linuxmodem

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/runtimereconcile"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestDataClaimRetainsATSnapshotAndCoreAPDUBlocker(t *testing.T) {
	const cardID = "8944100000000000001"
	current := &ownedDevice{
		usb: usbGeneration{AttachmentID: "attachment", Generation: "usb-generation"},
		lastFact: agentmodem.Fact{AttachmentID: "attachment", EquipmentID: "equipment",
			ContinuityEpoch: "usb-generation", SIM: agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: cardID, SessionGeneration: "sim-generation"}},
	}
	claim := &dataClaim{target: agentdata.Target{EquipmentID: "equipment", CardID: cardID},
		commandSnapshot: modemSnapshot{ObjectPath: "/org/freedesktop/ModemManager1/Modem/1"}, observedData: agentmodem.DataConnected}
	prober := &Prober{atSnapshot: map[string]agentat.Snapshot{}}
	for _, direct := range []bool{false, true} {
		at := agentat.Snapshot{State: "ready", Port: "mm-command", Detail: "call_capabilities_retrying",
			CallSignalling: false, SMS: true, SIMAPDU: direct, SIMAPDUOnDemand: true}
		prober.atSnapshot["attachment"] = at
		fact := prober.dataFact(current, claim)
		want := agentmodem.ATControlFact{State: agentmodem.ATControlReady, Port: at.Port, Detail: at.Detail,
			CallSignalling: at.CallSignalling, SMS: at.SMS, SIMAPDU: at.SIMAPDU, SIMAPDUOnDemand: at.SIMAPDUOnDemand}
		if fact.AT != want {
			t.Errorf("AT snapshot lost fields: got=%+v want=%+v", fact.AT, want)
		}
	}
	prober.atSnapshot["attachment"] = agentat.Snapshot{State: "ready", Port: "mm-command", SMS: true, SIMAPDUOnDemand: true}
	fact := prober.dataFact(current, claim)
	// Exercise the actual Linux projection and Core start admission together.
	// Only platform I/O and the stopped Provider are fixtures; no carrier is contacted.
	wireAT, err := json.Marshal(fact.AT)
	if err != nil {
		t.Fatal(err)
	}
	var at agentlink.ModemATControlFact
	if err := json.Unmarshal(wireAT, &at); err != nil {
		t.Fatal(err)
	}
	agents := projectedDataAgents{statuses: []agentlink.ConnectionStatus{{AgentID: "agent", ProcessGeneration: "process", LastReport: time.Now(),
		Topology: &agentlink.TopologySnapshot{ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
			AttachmentID: fact.AttachmentID, EquipmentID: fact.EquipmentID, Condition: string(fact.Condition), AT: at,
			SIM:     agentlink.ModemSIMFact{State: string(fact.SIM.State), ICCID: fact.SIM.ICCID, SessionGeneration: fact.SIM.SessionGeneration},
			Network: agentlink.ModemNetworkFact{Data: string(fact.Network.Data)},
			Policy:  &agentlink.ModemPolicyFact{EquipmentID: fact.EquipmentID, CardID: fact.SIM.ICCID},
		}}},
	}}}
	root := t.TempDir()
	catalog, err := linecatalog.Open(filepath.Join(root, "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	if _, err := catalog.Put(linecatalog.Line{ID: "line-1", Enabled: true, CardID: cardID,
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", SMSC: "+441234567890"}}); err != nil {
		t.Fatal(err)
	}
	store, err := events.OpenBoltStore(filepath.Join(root, "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	replay, err := events.NewReplay(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := runtimereconcile.New(runtimereconcile.Config{Context: t.Context(), Catalog: catalog,
		Agents: agents, Runtime: projectedStoppedRuntime{}, Store: store, Replay: replay})
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = reconciler.RequestIntent(ctx, "line-1", true, "projection-start")
	var operation *vowifiipc.OperationError
	if !errors.As(err, &operation) || operation.Code != "sim_apdu_data_active" || operation.Layer != "card_route" {
		t.Fatalf("Linux connected data did not preserve the typed APDU blocker: %v", err)
	}
	enabled, found, _, err := catalog.RuntimeIntent("line-1")
	if err != nil || !found || !enabled || claim.observedData != agentmodem.DataConnected || claim.cleanup {
		t.Fatal("admission lost desired intent or mutated the bearer")
	}
}

type projectedDataAgents struct{ statuses []agentlink.ConnectionStatus }

func (a projectedDataAgents) Statuses() []agentlink.ConnectionStatus { return a.statuses }
func (projectedDataAgents) ResolveModemTargetForCardAction(string, agentlink.ModemAction) (agentlink.ModemTarget, error) {
	return agentlink.ModemTarget{}, errors.New("no call/SMS route in projection fixture")
}
func (projectedDataAgents) ResolveModemDataTargetForCard(string) (agentlink.ModemTarget, error) {
	return agentlink.ModemTarget{}, errors.New("no data lease in projection fixture")
}

type projectedStoppedRuntime struct{}

func (projectedStoppedRuntime) Observe(context.Context, string) (vowifiipc.Snapshot, mediaauth.ProviderFence, error) {
	return vowifiipc.Snapshot{Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeStopped}}, mediaauth.ProviderFence{}, nil
}
func (projectedStoppedRuntime) Start(context.Context, string, mediaauth.ProviderFence, vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	return vowifiipc.OperationResult{}, errors.New("unexpected provider start")
}
func (projectedStoppedRuntime) Stop(context.Context, string, mediaauth.ProviderFence, vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	return vowifiipc.OperationResult{}, errors.New("unexpected provider stop")
}
