package agentpolicy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type controlHealthRuntime struct {
	*testRuntime
	failed bool
	checks int
}

func (runtime *controlHealthRuntime) ConnectionNeedsRecovery(context.Context, Target) (bool, error) {
	runtime.checks++
	return runtime.failed, nil
}

func TestForcedClosedControlRecoversThroughExistingOwnershipGate(t *testing.T) {
	manager, runtime, coordinator := testManager(t)
	health := &controlHealthRuntime{testRuntime: runtime, failed: true}
	manager.config.Runtime = health
	fact := runtime.facts[0]
	fact.Network.Data = agentmodem.DataConnected
	fact.AT.State = agentmodem.ATControlUnavailable
	target := agentdata.Target{EquipmentID: fact.EquipmentID, AttachmentID: fact.AttachmentID, CardID: fact.SIM.ICCID, SIMSessionGeneration: fact.SIM.SessionGeneration}
	connection := &testConnection{owned: []agentdata.Target{target}}
	if err := manager.BindConnection(connection); err != nil {
		t.Fatal(err)
	}
	coordinator.setActive(true)
	manager.reconcileConnectionOwners(context.Background(), []agentmodem.Fact{fact})
	if health.checks != 0 || connection.releases != 0 {
		t.Fatal("busy call permitted control recovery")
	}
	coordinator.setActive(false)
	manager.config.Now = func() time.Time { return time.Now().Add(time.Hour) }
	manager.reconcileConnectionOwners(context.Background(), []agentmodem.Fact{fact})
	if health.checks != 1 || connection.releases != 1 {
		t.Fatal("idle forced-closed control was not retired")
	}
}

func TestDisconnectedPersistentConnectionRecoversWithoutChangingIntent(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		manager, runtime, _ := testManager(t)
		fact := runtime.facts[0]
		fact.Network.Data = agentmodem.DataDisconnected
		policy := Policy{EquipmentID: fact.EquipmentID, CardID: fact.SIM.ICCID, Desired: Desired{ConnectionEnabled: enabled}}
		stored, err := manager.config.Store.PutExpected(policy, 0)
		if err != nil {
			t.Fatal(err)
		}
		target := agentdata.Target{EquipmentID: fact.EquipmentID, AttachmentID: fact.AttachmentID, CardID: fact.SIM.ICCID, SIMSessionGeneration: fact.SIM.SessionGeneration}
		connection := &testConnection{owned: []agentdata.Target{target}}
		if err := manager.BindConnection(connection); err != nil {
			t.Fatal(err)
		}
		manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
		if connection.releases != 1 || len(connection.calls) != 1 || connection.calls[0] != enabled {
			t.Fatalf("releases=%d enabled=%v calls=%v", connection.releases, enabled, connection.calls)
		}
		after, _, err := manager.config.Store.Get(fact.EquipmentID, fact.SIM.ICCID)
		if err != nil || after != stored {
			t.Fatal("recovery changed durable policy")
		}
	}
}

func TestConnectionRecoveryRetainsBusyOrUnknownOwnerAndBacksOff(t *testing.T) {
	manager, runtime, coordinator := testManager(t)
	now := time.Now()
	manager.config.Now = func() time.Time { return now }
	fact := runtime.facts[0]
	target := agentdata.Target{EquipmentID: fact.EquipmentID, AttachmentID: fact.AttachmentID, CardID: fact.SIM.ICCID, SIMSessionGeneration: fact.SIM.SessionGeneration}
	connection := &testConnection{owned: []agentdata.Target{target}, releaseErr: errors.New("cleanup not confirmed")}
	if err := manager.BindConnection(connection); err != nil {
		t.Fatal(err)
	}
	fact.Network.Data = agentmodem.DataUnknown
	if len(manager.reconcileConnectionOwners(context.Background(), []agentmodem.Fact{fact})) != 1 || connection.releases != 0 {
		t.Fatal("unknown bearer was treated as disconnected")
	}
	fact.Network.Data = agentmodem.DataDisconnected
	coordinator.setActive(true)
	manager.reconcileConnectionOwners(context.Background(), []agentmodem.Fact{fact})
	if connection.releases != 0 {
		t.Fatal("active operation was interrupted")
	}
	coordinator.setActive(false)
	now = now.Add(time.Hour)
	manager.reconcileConnectionOwners(context.Background(), []agentmodem.Fact{fact})
	manager.reconcileConnectionOwners(context.Background(), []agentmodem.Fact{fact})
	if connection.releases != 1 || len(connection.owned) != 1 {
		t.Fatal("failed cleanup lost ownership or retried without backoff")
	}
	before := manager.status[pair(fact.EquipmentID, fact.SIM.ICCID)]
	manager.ReconcilePolicies(context.Background(), []agentmodem.Fact{fact})
	after := manager.status[pair(fact.EquipmentID, fact.SIM.ICCID)]
	if after.RetryAt != before.RetryAt || after.Attempt != before.Attempt {
		t.Fatal("topology report postponed recovery without attempting it")
	}
}
