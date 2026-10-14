package linedeletion

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/allowance"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/notifications"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

func TestPermanentDeletionCleansEveryDurableLineStore(t *testing.T) {
	for _, deleteHistory := range []bool{true, false} {
		t.Run(fmt.Sprint(deleteHistory), func(t *testing.T) { permanentDeletionFlow(t, deleteHistory) })
	}
}

func permanentDeletionFlow(t *testing.T, deleteHistory bool) {
	root, now := t.TempDir(), time.Now().UTC()
	catalog, err := linecatalog.Open(filepath.Join(root, "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	line := linecatalog.Line{SchemaVersion: 1, ID: "line-integrated", Enabled: false,
		CardID: "8944100000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}}
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.SetDeletedExpected(line.ID, true, 2); err != nil {
		t.Fatal(err)
	}
	eventStore, err := events.OpenBoltStore(filepath.Join(root, "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer eventStore.Close()
	replay, _ := events.NewReplay(time.Minute)
	eventPurger, _ := events.NewLinePurger(eventStore, replay)
	if _, err := eventStore.Activate(events.Event{SchemaVersion: 1, EventID: "event-1", LineID: line.ID,
		ProducerRole: events.RoleAgent, ProducerID: "agent-1", Layer: state.LayerHardware,
		Condition: state.ConditionReady, Available: true, Code: "hardware_ready", Generation: "generation-1",
		Sequence: 1, ObservedAt: now}, now); err != nil {
		t.Fatal(err)
	}
	messages, err := providermessages.OpenStore(filepath.Join(root, "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer messages.Close()
	message := providermessages.Event{SchemaVersion: 1, EventID: "message-event", LineID: line.ID,
		ProviderID: "provider-1", ProcessGeneration: "generation-1", Kind: providermessages.KindReceived,
		ObservedAt: now, Sender: "+44123", Body: "hello"}
	if _, stored, err := messages.Accept(message, now); err != nil || !stored {
		t.Fatalf("message stored=%t err=%v", stored, err)
	}
	calls, err := callhistory.Open(filepath.Join(root, "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer calls.Close()
	if err := calls.Start(line.ID, "vowifi", "call-1", "out", "+44123", now); err != nil {
		t.Fatal(err)
	}
	if err := calls.Finish(line.ID, "vowifi", "call-1", "ended", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	allowances, err := allowance.Open(filepath.Join(root, "allowance.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer allowances.Close()
	if _, _, err := allowances.PutSnapshotExpected(line.ID, 1, allowance.Values{Balance: "5"}, now); err != nil {
		t.Fatal(err)
	}
	notificationStore, err := notifications.Open(filepath.Join(root, "notifications.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer notificationStore.Close()
	smsOperations, err := cellularmessages.OpenOperationStore(filepath.Join(root, "sms-operations.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer smsOperations.Close()
	sms := cellularmessages.OperationRecord{OperationID: "sms-operation", MessageID: "message-1", LineID: line.ID,
		EquipmentID: "123456789012345", CardID: line.CardID, AgentID: "agent-1", ProcessGeneration: "generation-1",
		AttachmentID: "attachment-1", Recipient: "+44123", BodySHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("hello"))), CreatedAt: now}
	if _, created, err := smsOperations.Begin(sms); err != nil || !created {
		t.Fatalf("SMS operation created=%t err=%v", created, err)
	}
	handler, err := NewHandler(Config{Catalog: catalog,
		Guard:         linecatalog.LifecycleGuardFunc(func(string) (bool, error) { return false, nil }),
		Notifications: notificationStore, Events: eventPurger, Allowance: allowances,
		Messages: messages, SMSOperations: smsOperations, Calls: calls, Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	deleteLine := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/catalog/lines/line-integrated/permanent-delete",
			strings.NewReader(fmt.Sprintf(`{"schema_version":1,"operation_id":"integrated-delete","delete_history":%t}`, deleteHistory)))
		request.SetPathValue("lineID", line.ID)
		request.Header.Set("If-Match", `"3"`)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, state := range []string{"prepared", "uncertain", "submitted"} {
		if _, err := smsOperations.Mark(sms.OperationID, state, []int{9}); err != nil {
			t.Fatal(err)
		}
		response := deleteLine()
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "line_sms_operation_unresolved") {
			t.Fatalf("UNRESOLVED_SMS_DELETION: %s status=%d body=%s", state, response.Code, response.Body.String())
		}
		if got, err := catalog.Get(line.ID); err != nil || !got.Deleted {
			t.Fatal("catalog removed before SMS protection", err)
		}
		if count, _ := eventStore.Count(); count != 1 {
			t.Fatal("events purged before SMS protection")
		}
		if records, _ := messages.List(line.ID, 10); len(records) != 1 {
			t.Fatal("messages purged before SMS protection")
		}
		if records, _ := calls.List(line.ID, 10); len(records) != 1 {
			t.Fatal("calls purged before SMS protection")
		}
		if snapshot, _ := allowances.Snapshot(line.ID); snapshot.Values.Balance != "5" {
			t.Fatal("allowance purged before SMS protection")
		}
		if _, found, _ := smsOperations.Get(sms.OperationID); !found {
			t.Fatal("unresolved SMS evidence erased")
		}
	}
	if _, _, err := messages.Accept(providermessages.Event{SchemaVersion: 1,
		EventID: "cellular-submit-sms-operation-1", LineID: line.ID, ProviderID: "cellular",
		ProcessGeneration: sms.ProcessGeneration, Kind: providermessages.KindSubmitted, ObservedAt: now,
		MessageID: sms.MessageID, Part: 1, Recipient: sms.Recipient, CallID: "cellular-mr-9", RPMR: 9,
		State: "submitted", Body: "hello"}, now); err != nil {
		t.Fatal(err)
	}
	response := deleteLine()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := catalog.Get(line.ID); err != linecatalog.ErrNotFound {
		t.Fatalf("catalog line err=%v", err)
	}
	if count, _ := eventStore.Count(); count != 0 {
		t.Fatalf("event count=%d", count)
	}
	wantMessages, wantCalls := 0, 0
	if !deleteHistory {
		wantMessages, wantCalls = 2, 1
	}
	if records, _ := messages.List(line.ID, 10); len(records) != wantMessages {
		t.Fatalf("message records=%+v", records)
	}
	if records, _ := calls.List(line.ID, 10); len(records) != wantCalls {
		t.Fatalf("call records=%+v", records)
	}
	if snapshot, _ := allowances.Snapshot(line.ID); snapshot.Revision != 1 || snapshot.Values.Balance != "" {
		t.Fatalf("allowance snapshot=%+v", snapshot)
	}
	if _, found, _ := smsOperations.Get(sms.OperationID); found == deleteHistory {
		t.Fatal("SMS history retention differs from confirmed intent")
	}
	sms.OperationID = "new-after-delete"
	if _, _, err := smsOperations.Begin(sms); err == nil {
		t.Fatal("deleted line accepted a new paid operation")
	}
	if repeated := deleteLine(); repeated.Code != http.StatusOK {
		t.Fatal("original deletion cannot be resumed", repeated.Body.String())
	}
}
