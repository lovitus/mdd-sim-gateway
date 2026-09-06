package callhistory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestProviderSnapshotsProduceOneAccurateCallRecord(t *testing.T) {
	store := openTestStore(t)
	started := time.Unix(1_800_000_000, 0).UTC()
	snapshot := readySnapshot(started)
	snapshot.PendingIncomingCall = &vowifiipc.PendingIncomingCall{
		CallID: "incoming-1", Caller: "+44123", Callee: "+44999", ReceivedAt: started,
	}
	if err := store.ObserveVoWiFiSnapshot(snapshot, "8944100000000000001", started.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	snapshot.Sequence++
	snapshot.ObservedAt = started.Add(2 * time.Second)
	snapshot.PendingIncomingCall = nil
	snapshot.ActiveCall = &vowifiipc.ActiveCall{CallID: "incoming-1", Condition: vowifiipc.CallActive}
	if err := store.ObserveVoWiFiSnapshot(snapshot, "8944100000000000001", snapshot.ObservedAt); err != nil {
		t.Fatal(err)
	}
	snapshot.Sequence++
	snapshot.ObservedAt = started.Add(32 * time.Second)
	snapshot.ActiveCall = nil
	if err := store.ObserveVoWiFiSnapshot(snapshot, "8944100000000000001", snapshot.ObservedAt); err != nil {
		t.Fatal(err)
	}

	records, err := store.List("line-1", 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	record := records[0]
	if record.Direction != "in" || record.Peer != "+44123" || record.Status != "ended" ||
		record.AnsweredAt == nil || record.EndedAt == nil || !record.StartedAt.Equal(started) {
		t.Fatalf("record=%+v", record)
	}
}

func TestPurgeLineRejectsActiveThenErasesEndedHistory(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	if err := store.Start("line-purge", "vowifi", "call-1", "out", "+44123", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PurgeLine("line-purge"); err == nil {
		t.Fatal("active call history was purged")
	}
	if err := store.Finish("line-purge", "vowifi", "call-1", "ended", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.PurgeLine("line-purge"); err != nil {
		t.Fatal(err)
	}
	if records, _ := store.List("line-purge", 10); len(records) != 0 {
		t.Fatalf("purged call records=%+v", records)
	}
	if err := store.Start("line-purge", "vowifi", "late-call", "out", "+44123", now.Add(2*time.Second)); err == nil {
		t.Fatal("purged line accepted a late call")
	}
}

func TestRetainLineKeepsEndedHistoryAndRejectsLateCalls(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	if err := store.Start("line-retain", "vowifi", "call-1", "out", "+44123", now); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish("line-retain", "vowifi", "call-1", "ended", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainLine("line-retain"); err != nil {
		t.Fatal(err)
	}
	if records, _ := store.List("line-retain", 10); len(records) != 1 {
		t.Fatalf("retained records=%+v", records)
	}
	if err := store.Start("line-retain", "vowifi", "call-late", "out", "+44123", now.Add(2*time.Second)); err == nil {
		t.Fatal("retired line accepted a late call")
	}
}

func TestCellularNotificationOutboxIsRollbackAdditive(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	stored, err := store.AcceptCellularEvent(CellularCallSource{IncomingEventID: "incoming-1",
		LastEventID: "event-1", Revision: 1, LineID: "line-1", AgentID: "agent-1",
		ProcessGeneration: "process-1", AttachmentID: "attachment-1", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", SIMSessionGeneration: "session-1", Occurrence: 1,
		NativeCallIndex: 4, State: "ringing_in", Direction: "in", Number: "+44123",
		FirstObservedAt: now, ObservedAt: now, ReceivedAt: now, Notify: true})
	if err != nil || !stored {
		t.Fatalf("stored=%t err=%v", stored, err)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		if keys := tx.Bucket(notificationSourceBucket).Stats().KeyN; keys != 0 {
			t.Fatalf("cellular source polluted legacy VoWiFi bucket: keys=%d", keys)
		}
		if keys := tx.Bucket(cellularNotificationSourceBucket).Stats().KeyN; keys != 1 {
			t.Fatalf("cellular source keys=%d", keys)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIncomingCallSourceRequiresAuthoritativeSnapshotWaitsAndAcksExactly(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := store.Start("line-1", "vowifi", "incoming-1", "in", "", now); err != nil {
		t.Fatal(err)
	}
	if sources, err := store.PendingNotificationSources(now.Add(time.Hour), 10); err != nil || len(sources) != 0 {
		t.Fatalf("browser preparation manufactured source=%+v err=%v", sources, err)
	}
	snapshot := readySnapshot(now)
	snapshot.PendingIncomingCall = &vowifiipc.PendingIncomingCall{
		CallID: "incoming-1", Caller: "+44123", Callee: "+44999", ReceivedAt: now,
	}
	if err := store.ObserveVoWiFiSnapshot(snapshot, "8944100000000000001", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if sources, err := store.PendingNotificationSources(now.Add(3*time.Second), 10); err != nil || len(sources) != 0 {
		t.Fatalf("source moved before caller window=%+v err=%v", sources, err)
	}
	sources, err := store.PendingNotificationSources(now.Add(5*time.Second), 10)
	if err != nil || len(sources) != 1 || sources[0].Peer != "+44123" || sources[0].Transport != "vowifi" ||
		sources[0].CardID != "8944100000000000001" {
		t.Fatalf("completed sources=%+v err=%v", sources, err)
	}
	if err := store.AckNotificationSource(sources[0].SourceID); err != nil {
		t.Fatal(err)
	}
	if sources, _ := store.PendingNotificationSources(now.Add(time.Hour), 10); len(sources) != 0 {
		t.Fatalf("acked call source remained=%+v", sources)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(notificationSourceBucket).ForEach(func(_, wire []byte) error {
			var source NotificationSource
			if json.Unmarshal(wire, &source) != nil || !source.Acked || source.Peer != "" || source.CardID != "" {
				t.Fatalf("acked source retained caller=%+v", source)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	snapshot.Sequence++
	snapshot.ObservedAt = now.Add(6 * time.Second)
	if err := store.ObserveVoWiFiSnapshot(snapshot, "8944100000000000001", snapshot.ObservedAt); err != nil {
		t.Fatal(err)
	}
	if sources, _ := store.PendingNotificationSources(now.Add(time.Hour), 10); len(sources) != 0 {
		t.Fatalf("repeated ringing snapshot recreated source=%+v", sources)
	}
}

func TestCompatibleProviderWithoutCardIDStillUpdatesCallHistoryWithoutNotification(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	snapshot := readySnapshot(now)
	snapshot.PendingIncomingCall = &vowifiipc.PendingIncomingCall{
		CallID: "incoming-compatible", Caller: "+44123", Callee: "+44999", ReceivedAt: now,
	}
	if err := store.ObserveVoWiFiSnapshot(snapshot, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	records, err := store.List("line-1", 10)
	if err != nil || len(records) != 1 || records[0].Status != "ringing" || records[0].Peer != "+44123" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if sources, err := store.PendingNotificationSources(now.Add(time.Hour), 10); err != nil || len(sources) != 0 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
}

func TestHistoryCannotDeleteAnOpenCallAndHTTPRejectsUnknownQueries(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := store.Start("line-1", "cellular", "call-1", "out", "+852123", now); err != nil {
		t.Fatal(err)
	}
	records, err := store.List("", 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if _, err := store.Delete([]string{records[0].ID}); err == nil {
		t.Fatal("open call history was deleted")
	}
	if err := store.Finish("line-1", "cellular", "call-1", "ended", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.Delete([]string{records[0].ID}); err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}

	handler, err := NewHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/calls?unexpected=true", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_call_history_query") {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
}

func TestProviderHeartbeatCannotRaceAnOutgoingCallStart(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := store.Start("line-1", "vowifi", "call-1", "out", "+44123", now); err != nil {
		t.Fatal(err)
	}
	snapshot := readySnapshot(now.Add(time.Second))
	if err := store.ObserveVoWiFiSnapshot(snapshot, "8944100000000000001", snapshot.ObservedAt); err != nil {
		t.Fatal(err)
	}
	records, err := store.List("line-1", 10)
	if err != nil || len(records) != 1 || records[0].Status != "dialing" || records[0].EndedAt != nil {
		t.Fatalf("outgoing setup was closed by a status heartbeat: records=%+v err=%v", records, err)
	}
	if err := store.Finish("line-1", "vowifi", "call-1", "failed", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	snapshot.ActiveCall = &vowifiipc.ActiveCall{CallID: "call-1", Condition: vowifiipc.CallActive}
	snapshot.Sequence++
	snapshot.ObservedAt = now.Add(3 * time.Second)
	if err := store.ObserveVoWiFiSnapshot(snapshot, "8944100000000000001", snapshot.ObservedAt); err != nil {
		t.Fatal(err)
	}
	records, err = store.List("line-1", 10)
	if err != nil || records[0].Status != "answered" || records[0].EndedAt != nil {
		t.Fatalf("authoritative active call did not repair an ambiguous failure: records=%+v err=%v", records, err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func readySnapshot(at time.Time) vowifiipc.Snapshot {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	return vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-1", ProviderID: "provider-1",
		ProcessGeneration: "generation-1", Sequence: 1, ObservedAt: at,
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning, Code: "ready"},
		Tunnel:  ready, IMS: ready, Voice: ready, Messaging: ready,
	}
}
