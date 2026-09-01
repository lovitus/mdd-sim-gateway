package cellularevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

type fakeCatalog struct{ snapshot linecatalog.Snapshot }

func (catalog fakeCatalog) Snapshot() (linecatalog.Snapshot, error) { return catalog.snapshot, nil }

type fakeAgents struct {
	status  agentlink.ConnectionStatus
	target  agentlink.ModemTarget
	resolve error
}

func (agents *fakeAgents) Status(id string) (agentlink.ConnectionStatus, bool) {
	return agents.status, id == agents.status.AgentID
}

func (agents *fakeAgents) ResolveModemTargetForAction(string, string, agentlink.ModemAction) (agentlink.ModemTarget, error) {
	return agents.target, agents.resolve
}

func cellularEventFixture(t *testing.T) (*Service, *fakeAgents, *providermessages.Store, *callhistory.Store) {
	t.Helper()
	root := t.TempDir()
	messages, err := providermessages.OpenStore(filepath.Join(root, "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := callhistory.Open(filepath.Join(root, "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = messages.Close(); _ = calls.Close() })
	cardID := "8985200000000000001"
	agents := &fakeAgents{target: agentlink.ModemTarget{AgentID: "agent-1", ProcessGeneration: "process-1",
		AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: cardID}}
	agents.status = agentlink.ConnectionStatus{AgentID: "agent-1", ProcessGeneration: "process-1",
		Topology: &agentlink.TopologySnapshot{ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
			AttachmentID: "attachment-1", EquipmentID: "862547055201716", Condition: "ready",
			AT:  agentlink.ModemATControlFact{State: "ready", CallSignalling: true, SMS: true},
			SIM: agentlink.ModemSIMFact{State: "ready", ICCID: cardID, SessionGeneration: "session-1"},
		}}}}
	service, err := New(fakeCatalog{snapshot: linecatalog.Snapshot{SchemaVersion: 1, Lines: []linecatalog.Line{{
		SchemaVersion: 1, ID: "line-1", CardID: cardID,
	}}}}, agents, messages, calls)
	if err != nil {
		t.Fatal(err)
	}
	return service, agents, messages, calls
}

func TestCellularEventsPersistBusinessFactsAndNotificationSources(t *testing.T) {
	service, _, messages, calls := cellularEventFixture(t)
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	source := agentlink.AgentEventContext{AgentID: "agent-1", ProcessGeneration: "process-1"}
	call := agentlink.ModemEvent{SchemaVersion: 1, EventID: "call-state-1", Kind: agentlink.ModemEventKindCall,
		AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "session-1", ObservedAt: now,
		Call: &agentlink.ModemEventCall{IncomingEventID: "incoming-1", Occurrence: 1, Revision: 1,
			NativeIndex: 4, State: "ringing_in", Direction: "in", Number: "+44123",
			FirstObservedAt: now, Notify: true}}
	if outcome := service.AcceptModemEvent(context.Background(), source, call); !outcome.Accepted {
		t.Fatalf("call outcome=%+v", outcome)
	}
	current, err := calls.CurrentCellularCalls()
	if err != nil || len(current) != 1 || current[0].IncomingEventID != "incoming-1" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	notifications, err := calls.PendingNotificationSources(now.Add(5*time.Second), 10)
	if err != nil || len(notifications) != 1 || notifications[0].Transport != "cellular" {
		t.Fatalf("call notifications=%+v err=%v", notifications, err)
	}
	if outcome := service.AcceptModemEvent(context.Background(), source, call); !outcome.Accepted {
		t.Fatalf("duplicate call outcome=%+v", outcome)
	}

	sms := agentlink.ModemEvent{SchemaVersion: 1, EventID: "sms-event-1", Kind: agentlink.ModemEventKindSMS,
		AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "session-1", ObservedAt: now,
		SMS: &agentlink.ModemEventSMS{Index: 1, StorageIndices: []int{1}, Fingerprint: digest("sms-1"), State: "received", Direction: "in",
			Peer: "+44204", Body: "hello"}}
	if outcome := service.AcceptModemEvent(context.Background(), source, sms); !outcome.Accepted {
		t.Fatalf("SMS outcome=%+v", outcome)
	}
	records, err := messages.List("line-1", 10)
	if err != nil || len(records) != 1 || records[0].Body != "hello" {
		t.Fatalf("messages=%+v err=%v", records, err)
	}
	smsNotifications, err := messages.PendingNotificationSources(10)
	if err != nil || len(smsNotifications) != 1 || smsNotifications[0].Transport != "cellular" {
		t.Fatalf("SMS notifications=%+v err=%v", smsNotifications, err)
	}
}

func TestCellularEventFenceIsRetryableUntilHealthCatchesUp(t *testing.T) {
	service, agents, _, _ := cellularEventFixture(t)
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	event := agentlink.ModemEvent{SchemaVersion: 1, EventID: "sms-event-2", Kind: agentlink.ModemEventKindSMS,
		AttachmentID: "new-attachment", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "new-session", ObservedAt: now,
		SMS: &agentlink.ModemEventSMS{Index: 2, StorageIndices: []int{2}, Fingerprint: digest("sms-2"), State: "received", Direction: "in",
			Peer: "+44204", Body: "hello"}}
	outcome := service.AcceptModemEvent(context.Background(), agentlink.AgentEventContext{
		AgentID: "agent-1", ProcessGeneration: "process-1",
	}, event)
	if outcome.Accepted || !outcome.Retryable || outcome.Code != "modem_event_topology_not_ready" {
		t.Fatalf("outcome=%+v", outcome)
	}
	agents.status.ProcessGeneration = "other-process"
	outcome = service.AcceptModemEvent(context.Background(), agentlink.AgentEventContext{
		AgentID: "agent-1", ProcessGeneration: "process-1",
	}, event)
	if outcome.Accepted || outcome.Retryable || outcome.Code != "stale_modem_event_process" {
		t.Fatalf("stale process outcome=%+v", outcome)
	}
}

func TestTerminalFirstTombstonePreventsRingingReplay(t *testing.T) {
	service, agents, _, calls := cellularEventFixture(t)
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	source := agentlink.AgentEventContext{AgentID: "agent-1", ProcessGeneration: "process-1"}
	ring := agentlink.ModemEvent{SchemaVersion: 1, EventID: "call-ring-retry", Kind: agentlink.ModemEventKindCall,
		AttachmentID: "attachment-new", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "session-new", ObservedAt: now,
		Call: &agentlink.ModemEventCall{IncomingEventID: "incoming-terminal-first", Occurrence: 7, Revision: 1,
			NativeIndex: 5, State: "ringing_in", Direction: "in", Number: "+44123",
			FirstObservedAt: now, Notify: true}}
	if outcome := service.AcceptModemEvent(context.Background(), source, ring); !outcome.Retryable || outcome.Accepted {
		t.Fatalf("ring outcome=%+v", outcome)
	}
	terminal := ring
	terminal.EventID = "call-idle-accepted"
	terminal.Call = &agentlink.ModemEventCall{IncomingEventID: ring.Call.IncomingEventID,
		Occurrence: ring.Call.Occurrence, Revision: 2, NativeIndex: ring.Call.NativeIndex,
		State: "idle", Direction: "in", Number: ring.Call.Number, FirstObservedAt: ring.Call.FirstObservedAt}
	terminal.ObservedAt = now.Add(time.Second)
	if outcome := service.AcceptModemEvent(context.Background(), source, terminal); !outcome.Accepted {
		t.Fatalf("terminal outcome=%+v", outcome)
	}
	agents.target.AttachmentID = ring.AttachmentID
	agents.status.Topology.Modems[0].AttachmentID = ring.AttachmentID
	agents.status.Topology.Modems[0].SIM.SessionGeneration = ring.SIMSessionGeneration
	if outcome := service.AcceptModemEvent(context.Background(), source, ring); !outcome.Accepted {
		t.Fatalf("ring replay outcome=%+v", outcome)
	}
	current, err := calls.CurrentCellularCalls()
	if err != nil || len(current) != 0 {
		t.Fatalf("terminal-first replay created phantom current=%+v err=%v", current, err)
	}
}

func TestExactDuplicateRefreshesLiveReceiptWithoutDuplicateNotification(t *testing.T) {
	service, _, _, calls := cellularEventFixture(t)
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	source := agentlink.AgentEventContext{AgentID: "agent-1", ProcessGeneration: "process-1"}
	ring := agentlink.ModemEvent{SchemaVersion: 1, EventID: "call-ring-ack-loss", Kind: agentlink.ModemEventKindCall,
		AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "session-1", ObservedAt: now,
		Call: &agentlink.ModemEventCall{IncomingEventID: "incoming-ack-loss", Occurrence: 8, Revision: 1,
			NativeIndex: 6, State: "ringing_in", Direction: "in", Number: "+44123",
			FirstObservedAt: now, Notify: true}}
	if outcome := service.AcceptModemEvent(context.Background(), source, ring); !outcome.Accepted {
		t.Fatalf("first outcome=%+v", outcome)
	}
	now = now.Add(5 * time.Second)
	if outcome := service.AcceptModemEvent(context.Background(), source, ring); !outcome.Accepted {
		t.Fatalf("duplicate outcome=%+v", outcome)
	}
	current, err := calls.CurrentCellularCalls()
	if err != nil || len(current) != 1 || !current[0].ReceivedAt.Equal(now) {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	notifications, err := calls.PendingNotificationSources(now.Add(time.Second), 10)
	if err != nil || len(notifications) != 1 {
		t.Fatalf("notifications=%+v err=%v", notifications, err)
	}
}

func TestSamePDUOnTwoCardsCreatesTwoBusinessFacts(t *testing.T) {
	serviceA, _, messages, calls := cellularEventFixture(t)
	now := time.Now().UTC()
	serviceA.now = func() time.Time { return now }
	fingerprint := digest("same-pdu")
	eventA := agentlink.ModemEvent{SchemaVersion: 1, EventID: "agent-sms-card-a", Kind: agentlink.ModemEventKindSMS,
		AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "session-1", ObservedAt: now,
		SMS: &agentlink.ModemEventSMS{Index: 1, StorageIndices: []int{1}, Fingerprint: fingerprint,
			State: "received", Direction: "in", Peer: "+44123", Body: "same body"}}
	if outcome := serviceA.AcceptModemEvent(context.Background(), agentlink.AgentEventContext{
		AgentID: "agent-1", ProcessGeneration: "process-1",
	}, eventA); !outcome.Accepted {
		t.Fatalf("card A outcome=%+v", outcome)
	}
	cardB := "8985200000000000002"
	agentsB := &fakeAgents{target: agentlink.ModemTarget{AgentID: "agent-2", ProcessGeneration: "process-2",
		AttachmentID: "attachment-2", EquipmentID: "862547055201717", CardID: cardB}}
	agentsB.status = agentlink.ConnectionStatus{AgentID: "agent-2", ProcessGeneration: "process-2",
		Topology: &agentlink.TopologySnapshot{ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
			AttachmentID: "attachment-2", EquipmentID: "862547055201717", Condition: "ready",
			AT:  agentlink.ModemATControlFact{State: "ready", SMS: true},
			SIM: agentlink.ModemSIMFact{State: "ready", ICCID: cardB, SessionGeneration: "session-2"},
		}}}}
	serviceB, err := New(fakeCatalog{snapshot: linecatalog.Snapshot{SchemaVersion: 1, Lines: []linecatalog.Line{{
		SchemaVersion: 1, ID: "line-2", CardID: cardB,
	}}}}, agentsB, messages, calls)
	if err != nil {
		t.Fatal(err)
	}
	serviceB.now = func() time.Time { return now }
	eventB := eventA
	eventB.EventID, eventB.AttachmentID, eventB.EquipmentID, eventB.CardID, eventB.SIMSessionGeneration =
		"agent-sms-card-b", "attachment-2", "862547055201717", cardB, "session-2"
	if outcome := serviceB.AcceptModemEvent(context.Background(), agentlink.AgentEventContext{
		AgentID: "agent-2", ProcessGeneration: "process-2",
	}, eventB); !outcome.Accepted {
		t.Fatalf("card B outcome=%+v", outcome)
	}
	records, err := messages.List("", 10)
	if err != nil || len(records) != 2 || records[0].EventID == records[1].EventID {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	sources, err := messages.PendingNotificationSources(10)
	if err != nil || len(sources) != 2 || sources[0].CardID == sources[1].CardID {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
