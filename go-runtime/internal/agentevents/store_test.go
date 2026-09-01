package agentevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func testEventStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir()+"/modem-events.db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.MarkReady()
	return store
}

func testFence() Fence {
	return Fence{AttachmentID: "attachment-1", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", SIMSessionGeneration: "session-1"}
}

func smsFact(fingerprint, body string) agentmodem.SMSMessage {
	return agentmodem.SMSMessage{Index: 1, Indices: []int{1}, State: "received", Direction: "in", Peer: "+44123",
		Body: body, ObservedAt: time.Unix(1_800_000_000, 0).UTC(), Fingerprint: fingerprint}
}

func TestSMSBaselineSeenAndOutboxAreDurable(t *testing.T) {
	path := t.TempDir() + "/modem-events.db"
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	store.MarkReady()
	first := smsFact(digest("first"), "historical")
	if err := store.ObserveSMS(testFence(), []agentmodem.SMSMessage{first}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if events, _ := store.PendingModemEvents(time.Now(), 10); len(events) != 0 {
		t.Fatalf("first inventory replayed history: %+v", events)
	}
	second := smsFact(digest("second"), "new message")
	second.Index, second.Indices = 2, []int{2}
	if err := store.ObserveSMS(testFence(), []agentmodem.SMSMessage{first, second}, time.Now()); err != nil {
		t.Fatal(err)
	}
	events, err := store.PendingModemEvents(time.Now(), 10)
	if err != nil || len(events) != 1 || events[0].SMS == nil || events[0].SMS.Body != "new message" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if err := store.AckModemEvent(events[0].EventID); err != nil {
		t.Fatal(err)
	}
	deletion, found, err := store.PendingSMSDeletion([]Fence{testFence()})
	if err != nil || !found || len(deletion.Indices) != 1 || deletion.Indices[0] != 2 || deletion.Fingerprint != second.Fingerprint {
		t.Fatalf("deletion=%+v found=%t err=%v", deletion, found, err)
	}
	if err := store.CompleteSMSDeletion(deletion.CardID, deletion.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.MarkReady()
	if err := reopened.ObserveSMS(testFence(), []agentmodem.SMSMessage{first, second}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if events, _ := reopened.PendingModemEvents(time.Now(), 10); len(events) != 0 {
		t.Fatalf("acked SMS replayed after restart: %+v", events)
	}
	binary := smsFact(digest("binary"), "bad\x00payload")
	binary.Index, binary.Indices = 3, []int{3}
	if err := reopened.ObserveSMS(testFence(), []agentmodem.SMSMessage{binary}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if events, _ := reopened.PendingModemEvents(time.Now(), 10); len(events) != 0 {
		t.Fatalf("binary SMS entered user outbox: %+v", events)
	}
	if deletion, found, err := reopened.PendingSMSDeletion([]Fence{testFence()}); err != nil || found {
		t.Fatalf("non-displayable SMS was scheduled for deletion without Core ACK: deletion=%+v found=%t err=%v", deletion, found, err)
	}
}

func TestNonDisplayableSMSIsNeverDeletedWithoutCoreAck(t *testing.T) {
	store := testEventStore(t)
	first := smsFact(digest("binary-baseline"), "first\x00payload")
	first.Index, first.Indices = 7, []int{7}
	if err := store.ObserveSMS(testFence(), []agentmodem.SMSMessage{first}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if deletion, found, err := store.PendingSMSDeletion([]Fence{testFence()}); err != nil || found {
		t.Fatalf("baseline non-displayable deletion=%+v found=%t err=%v", deletion, found, err)
	}
	second := smsFact(digest("binary-later"), "second\u0080payload")
	second.Index, second.Indices = 8, []int{8}
	if err := store.ObserveSMS(testFence(), []agentmodem.SMSMessage{first, second}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if events, _ := store.PendingModemEvents(time.Now(), 10); len(events) != 0 {
		t.Fatalf("non-displayable SMS entered Core event outbox: %+v", events)
	}
	if deletion, found, err := store.PendingSMSDeletion([]Fence{testFence()}); err != nil || found {
		t.Fatalf("later non-displayable deletion=%+v found=%t err=%v", deletion, found, err)
	}
}

func TestSamePDUFingerprintOnDifferentCardsHasDifferentEventIdentity(t *testing.T) {
	store := testEventStore(t)
	fenceA := testFence()
	fenceB := Fence{AttachmentID: "attachment-2", EquipmentID: "862547055201717",
		CardID: "8985200000000000002", SIMSessionGeneration: "session-2"}
	baselineA, baselineB := smsFact(digest("baseline-a"), "old A"), smsFact(digest("baseline-b"), "old B")
	if err := store.ObserveSMS(fenceA, []agentmodem.SMSMessage{baselineA}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveSMS(fenceB, []agentmodem.SMSMessage{baselineB}, time.Now()); err != nil {
		t.Fatal(err)
	}
	sameA, sameB := smsFact(digest("same-pdu"), "same"), smsFact(digest("same-pdu"), "same")
	sameA.Index, sameA.Indices = 2, []int{2}
	sameB.Index, sameB.Indices = 3, []int{3}
	if err := store.ObserveSMS(fenceA, []agentmodem.SMSMessage{sameA}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveSMS(fenceB, []agentmodem.SMSMessage{sameB}, time.Now()); err != nil {
		t.Fatal(err)
	}
	events, err := store.PendingModemEvents(time.Now(), 10)
	if err != nil || len(events) != 2 || events[0].EventID == events[1].EventID {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestIncomingOccurrenceAndExactFence(t *testing.T) {
	store := testEventStore(t)
	now := time.Now().UTC()
	ring := agentmodem.CallResult{State: "ringing_in", Direction: "in", Number: "+85222333322",
		NativeIndex: 4, VoiceCalls: 1, IncomingCalls: 1, ObservedAt: now, Authoritative: true}
	if err := store.ObserveCall(testFence(), ring, now); err != nil {
		t.Fatal(err)
	}
	events, _ := store.PendingModemEvents(now, 10)
	if len(events) != 1 || events[0].Call == nil || events[0].Call.Notify {
		t.Fatalf("startup ringing must be current-only: %+v", events)
	}
	firstID := events[0].Call.IncomingEventID
	if err := store.AckModemEvent(events[0].EventID); err != nil {
		t.Fatal(err)
	}
	idle := agentmodem.CallResult{State: "idle", ObservedAt: now.Add(time.Second), Authoritative: true}
	if err := store.ObserveCall(testFence(), idle, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = store.PendingModemEvents(now, 10)
	if len(events) != 1 || events[0].Call.State != "idle" {
		t.Fatalf("terminal event=%+v", events)
	}
	_ = store.AckModemEvent(events[0].EventID)
	ring.ObservedAt = now.Add(2 * time.Second)
	if err := store.ObserveCall(testFence(), ring, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = store.PendingModemEvents(now, 10)
	if len(events) != 1 || events[0].Call.IncomingEventID == firstID || !events[0].Call.Notify || events[0].Call.Occurrence != 2 {
		t.Fatalf("new occurrence=%+v", events)
	}
	call := events[0].Call
	if err := store.RequireIncomingCall(agentmodem.IncomingCallFence{EventID: call.IncomingEventID,
		AttachmentID: testFence().AttachmentID, EquipmentID: testFence().EquipmentID,
		CardID: testFence().CardID, SIMSessionGeneration: testFence().SIMSessionGeneration,
		NativeCallIndex: call.NativeIndex, CallOccurrence: call.Occurrence, Number: ring.Number}); err != nil {
		t.Fatal(err)
	}
	if err := store.RequireIncomingCall(agentmodem.IncomingCallFence{EventID: firstID,
		AttachmentID: testFence().AttachmentID, EquipmentID: testFence().EquipmentID,
		CardID: testFence().CardID, SIMSessionGeneration: testFence().SIMSessionGeneration,
		NativeCallIndex: call.NativeIndex, CallOccurrence: call.Occurrence}); err == nil {
		t.Fatal("stale incoming event was accepted")
	}
}

func TestHotplugEmitsUnavailableWithoutRebindingOldCall(t *testing.T) {
	store := testEventStore(t)
	now := time.Now().UTC()
	idle := agentmodem.CallResult{State: "idle", ObservedAt: now, Authoritative: true}
	if err := store.ObserveCall(testFence(), idle, now); err != nil {
		t.Fatal(err)
	}
	ring := agentmodem.CallResult{State: "ringing_in", Direction: "in", Number: "+44123",
		NativeIndex: 3, VoiceCalls: 1, IncomingCalls: 1, ObservedAt: now.Add(time.Second), Authoritative: true}
	if err := store.ObserveCall(testFence(), ring, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ := store.PendingModemEvents(now, 10)
	if len(events) != 1 {
		t.Fatalf("ring events=%+v", events)
	}
	_ = store.AckModemEvent(events[0].EventID)
	if err := store.ReconcileFences(agentlink.TopologySnapshot{ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{}}); err != nil {
		t.Fatal(err)
	}
	events, _ = store.PendingModemEvents(now, 10)
	if len(events) != 1 || events[0].Call == nil || events[0].Call.State != "unavailable" {
		t.Fatalf("detach events=%+v", events)
	}
}

func TestRestartDoesNotReplayPersistedRingingBeforeFreshCLCC(t *testing.T) {
	path := t.TempDir() + "/modem-events.db"
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	store.MarkReady()
	now := time.Now().UTC()
	ring := agentmodem.CallResult{State: "ringing_in", Direction: "in", Number: "+44123",
		NativeIndex: 5, VoiceCalls: 1, IncomingCalls: 1, ObservedAt: now, Authoritative: true}
	if err := store.ObserveCall(testFence(), ring, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.ReconcileFences(scannerTopology()); err != nil {
		t.Fatal(err)
	}
	if events, _ := reopened.PendingModemEvents(now, 10); len(events) != 0 {
		t.Fatalf("persisted ringing sent before fresh CLCC: %+v", events)
	}
	idle := agentmodem.CallResult{State: "idle", ObservedAt: now.Add(time.Second), Authoritative: true}
	if err := reopened.ObserveCall(testFence(), idle, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ := reopened.PendingModemEvents(now, 10)
	if len(events) != 1 || events[0].Call == nil || events[0].Call.State != "idle" {
		t.Fatalf("fresh idle did not replace stale ringing: %+v", events)
	}
}

type fakeCoordinator struct{ blocked bool }

func (coordinator fakeCoordinator) DoBackgroundScan(ctx context.Context, callback func(context.Context) error) error {
	if coordinator.blocked {
		return agentcall.ErrAuxiliaryDuringCall
	}
	return callback(ctx)
}

type fakeScanOperator struct {
	mu       sync.Mutex
	call     agentmodem.CallResult
	blockSMS bool
	actions  []agentmodem.OperationAction
}

func (operator *fakeScanOperator) Operate(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	operator.mu.Lock()
	operator.actions = append(operator.actions, operation.Action)
	operator.mu.Unlock()
	if operation.Action == agentmodem.OperationCallStatus {
		return agentmodem.OperationResult{Call: operator.call}, nil
	}
	if operator.blockSMS {
		<-ctx.Done()
		return agentmodem.OperationResult{}, ctx.Err()
	}
	return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "listed", Messages: []agentmodem.SMSMessage{}}}, nil
}

func scannerTopology() agentlink.TopologySnapshot {
	return agentlink.TopologySnapshot{ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
		AttachmentID: testFence().AttachmentID, EquipmentID: testFence().EquipmentID, Condition: "ready",
		AT: agentlink.ModemATControlFact{State: "ready", CallSignalling: true, SMS: true},
		SIM: agentlink.ModemSIMFact{State: "ready", ICCID: testFence().CardID,
			SessionGeneration: testFence().SIMSessionGeneration},
	}}}
}

func TestScannerGloballyYieldsToPaidLeaseAndBoundsCMGL(t *testing.T) {
	store := testEventStore(t)
	operator := &fakeScanOperator{call: agentmodem.CallResult{State: "idle", ObservedAt: time.Now(), Authoritative: true}}
	scanner, err := NewScanner(ScannerConfig{Store: store, Operator: operator,
		Coordinator: fakeCoordinator{blocked: true}, Topology: scannerTopology, Every: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(operator.actions) != 0 {
		t.Fatalf("scanner issued AT during paid lease: %v", operator.actions)
	}

	operator.blockSMS = true
	scanner.config.Coordinator = fakeCoordinator{}
	started := time.Now()
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 2500*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("CMGL budget elapsed=%s", elapsed)
	}
	if len(operator.actions) != 2 || operator.actions[0] != agentmodem.OperationCallStatus ||
		operator.actions[1] != agentmodem.OperationSMSList {
		t.Fatalf("scanner ordering=%v", operator.actions)
	}
}

func TestScannerDeletesOnlyAfterCoreAcknowledgement(t *testing.T) {
	store := testEventStore(t)
	first := smsFact(digest("baseline-delete"), "old")
	second := smsFact(digest("new-delete"), "new")
	second.Index, second.Indices = 2, []int{2}
	if err := store.ObserveSMS(testFence(), []agentmodem.SMSMessage{first}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveSMS(testFence(), []agentmodem.SMSMessage{first, second}, time.Now()); err != nil {
		t.Fatal(err)
	}
	events, _ := store.PendingModemEvents(time.Now(), 10)
	if len(events) != 1 {
		t.Fatalf("events=%+v", events)
	}
	if _, found, _ := store.PendingSMSDeletion([]Fence{testFence()}); found {
		t.Fatal("SMS deletion was queued before Core acknowledgement")
	}
	if err := store.AckModemEvent(events[0].EventID); err != nil {
		t.Fatal(err)
	}
	operator := &fakeScanOperator{call: agentmodem.CallResult{State: "idle", ObservedAt: time.Now(), Authoritative: true}}
	scanner, err := NewScanner(ScannerConfig{Store: store, Operator: operator,
		Coordinator: fakeCoordinator{}, Topology: scannerTopology, Every: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := scanner.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.PendingSMSDeletion([]Fence{testFence()}); err != nil || found {
		t.Fatalf("deletion remained found=%t err=%v", found, err)
	}
	if len(operator.actions) != 2 || operator.actions[0] != agentmodem.OperationCallStatus ||
		operator.actions[1] != agentmodem.OperationSMSDelete {
		t.Fatalf("actions=%v", operator.actions)
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
