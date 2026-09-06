package providermessages

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestStorePersistsAndDeduplicatesExactProviderEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	store, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	event := validEvent()
	if _, stored, err := store.Accept(event, time.Now()); err != nil || !stored {
		t.Fatalf("first Accept() stored=%v err=%v", stored, err)
	}
	if _, stored, err := store.Accept(event, time.Now().Add(time.Second)); err != nil || stored {
		t.Fatalf("duplicate Accept() stored=%v err=%v", stored, err)
	}
	adopted := event
	adopted.ProcessGeneration = "generation-2"
	if _, stored, err := store.Accept(adopted, time.Now().Add(2*time.Second)); err != nil || stored {
		t.Fatalf("adopted retry Accept() stored=%v err=%v", stored, err)
	}
	conflict := event
	conflict.Body = "different"
	if _, _, err := store.Accept(conflict, time.Now()); err != ErrConflict {
		t.Fatalf("conflicting Accept() err=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	records, err := reopened.List("line-1", 10)
	if err != nil || len(records) != 1 || records[0].Body != "hello" {
		t.Fatalf("List()=%+v err=%v", records, err)
	}
}

func TestMessageTransportFilteringAndHistoryDeletionKeepReplayReceipt(t *testing.T) {
	store := openStoreForCellularTest(t)
	now := time.Now().UTC()
	vowifi := validEvent()
	vowifi.EventID, vowifi.Sender = "vowifi-message", "+44111"
	cellular := validEvent()
	cellular.EventID, cellular.ProviderID, cellular.ProcessGeneration, cellular.Sender =
		"cellular-"+strings.Repeat("a", 64), "cellular", "agent-generation", "+44222"
	if _, stored, err := store.Accept(vowifi, now); err != nil || !stored {
		t.Fatalf("VoWiFi accept stored=%t err=%v", stored, err)
	}
	if _, stored, err := store.Accept(cellular, now.Add(time.Second)); err != nil || !stored {
		t.Fatalf("cellular accept stored=%t err=%v", stored, err)
	}
	cellularOnly, err := store.ListTransport("line-1", "cellular", 10)
	if err != nil || len(cellularOnly) != 1 || cellularOnly[0].Transport != "cellular" || cellularOnly[0].Sender != "+44222" {
		t.Fatalf("cellular records=%+v err=%v", cellularOnly, err)
	}
	deleted, err := store.DeleteHistory("line-1", "cellular", "+44222", nil, false)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if records, _ := store.List("line-1", 10); len(records) != 1 || records[0].EventID != vowifi.EventID {
		t.Fatalf("remaining records=%+v", records)
	}
	if _, stored, err := store.Accept(cellular, now.Add(2*time.Second)); err != nil || stored {
		t.Fatalf("deleted history replayed stored=%t err=%v", stored, err)
	}
	if records, _ := store.ListTransport("line-1", "cellular", 10); len(records) != 0 {
		t.Fatalf("deleted cellular history reappeared=%+v", records)
	}
}

func TestPurgeLineErasesPayloadButKeepsReplayTombstone(t *testing.T) {
	store := openStoreForCellularTest(t)
	now := time.Now().UTC()
	event := validEvent()
	if _, stored, err := store.AcceptWithNotification(event, "8944100000000000001", now); err != nil || !stored {
		t.Fatalf("accept stored=%t err=%v", stored, err)
	}
	if err := store.PurgeLine(event.LineID); err != nil {
		t.Fatal(err)
	}
	if records, _ := store.List(event.LineID, 10); len(records) != 0 {
		t.Fatalf("purged records=%+v", records)
	}
	if sources, _ := store.PendingNotificationSources(10); len(sources) != 0 {
		t.Fatalf("purged notification payload=%+v", sources)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketIDs).Stats().KeyN != 0 {
			t.Fatal("full-event deduplication value remains after purge")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, stored, err := store.Accept(event, now.Add(time.Second)); err == nil || stored {
		t.Fatalf("purged line accepted replay stored=%t err=%v", stored, err)
	}
}

func TestRetainLineKeepsHistoryButDropsNotificationPayload(t *testing.T) {
	store := openStoreForCellularTest(t)
	now := time.Now().UTC()
	event := validEvent()
	if _, stored, err := store.AcceptWithNotification(event, "8944100000000000001", now); err != nil || !stored {
		t.Fatalf("accept stored=%t err=%v", stored, err)
	}
	if err := store.RetainLine(event.LineID); err != nil {
		t.Fatal(err)
	}
	if records, _ := store.List(event.LineID, 10); len(records) != 1 {
		t.Fatalf("retained records=%+v", records)
	}
	if sources, _ := store.PendingNotificationSources(10); len(sources) != 0 {
		t.Fatalf("retained notification payload=%+v", sources)
	}
	event.EventID = "late-retained-event"
	if _, _, err := store.Accept(event, now.Add(time.Second)); err == nil {
		t.Fatal("retired line accepted a late message")
	}
}

func TestOnlyRealtimeProviderIngressCreatesNotificationSource(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	cellularInventory := validEvent()
	cellularInventory.EventID = "cellular-old-inventory"
	if _, stored, err := store.Accept(cellularInventory, now); err != nil || !stored {
		t.Fatalf("cellular inventory stored=%t err=%v", stored, err)
	}
	if sources, err := store.PendingNotificationSources(10); err != nil || len(sources) != 0 {
		t.Fatalf("old cellular inventory produced notification sources=%+v err=%v", sources, err)
	}
	realtime := validEvent()
	realtime.EventID = "vowifi-realtime"
	if _, stored, err := store.AcceptWithNotification(realtime, "8944100000000000001", now.Add(time.Second)); err != nil || !stored {
		t.Fatalf("provider ingress stored=%t err=%v", stored, err)
	}
	sources, err := store.PendingNotificationSources(10)
	if err != nil || len(sources) != 1 || sources[0].Transport != "vowifi" || sources[0].Body != "hello" ||
		sources[0].CardID != "8944100000000000001" {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	if _, stored, err := store.AcceptWithNotification(realtime, "8944100000000000001", now.Add(2*time.Second)); err != nil || stored {
		t.Fatalf("duplicate provider ingress stored=%t err=%v", stored, err)
	}
	if sources, _ := store.PendingNotificationSources(10); len(sources) != 1 {
		t.Fatalf("duplicate source count=%d", len(sources))
	}
	if err := store.AckNotificationSource(sources[0].SourceID); err != nil {
		t.Fatal(err)
	}
	if sources, _ := store.PendingNotificationSources(10); len(sources) != 0 {
		t.Fatalf("acked source remained=%+v", sources)
	}
	emptyBody := validEvent()
	emptyBody.EventID, emptyBody.MessageID, emptyBody.Body = "vowifi-empty-body", "binary-message", ""
	if _, stored, err := store.AcceptWithNotification(emptyBody, "8944100000000000001", now.Add(3*time.Second)); err != nil || !stored {
		t.Fatalf("empty-body received fact stored=%t err=%v", stored, err)
	}
	if sources, err := store.PendingNotificationSources(10); err != nil || len(sources) != 1 || sources[0].Body != "" {
		t.Fatalf("empty-body sources=%+v err=%v", sources, err)
	}
}

func TestCellularNotificationOutboxIsRollbackAdditive(t *testing.T) {
	store := openStoreForCellularTest(t)
	now := time.Now().UTC()
	event := Event{SchemaVersion: SchemaVersion, EventID: "cellular-" + strings.Repeat("a", 64),
		LineID: "line-1", ProviderID: "cellular", ProcessGeneration: "agent-generation-1",
		Kind: KindReceived, ObservedAt: now, MessageID: strings.Repeat("a", 64), Sender: "+44123", Body: "hello"}
	if _, _, err := store.AcceptWithNotificationTransport(event, "8985200000000000001", "cellular", now); err != nil {
		t.Fatal(err)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		if stats := tx.Bucket(bucketNotify).Stats(); stats.KeyN != 0 {
			t.Fatalf("cellular source polluted legacy VoWiFi bucket: keys=%d", stats.KeyN)
		}
		if stats := tx.Bucket(bucketCellularNotify).Stats(); stats.KeyN != 1 {
			t.Fatalf("cellular source keys=%d", stats.KeyN)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sources, err := store.PendingNotificationSources(10)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	if err := store.AckNotificationSource(sources[0].SourceID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcceptWithNotificationTransport(event, "8985200000000000001", "cellular", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if replay, _ := store.PendingNotificationSources(10); len(replay) != 0 {
		t.Fatalf("acked cellular notification was recreated: %+v", replay)
	}
}

func openStoreForCellularTest(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestMessageStoreAddsOutboxWithoutChangingRollbackCompatibleSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	legacyEvent := validEvent()
	legacyRecord, _ := json.Marshal(Record{Event: legacyEvent, ReceivedAt: time.Unix(1_800_000_000, 0).UTC()})
	legacyValues := map[string][]byte{
		string(bucketRecords): legacyRecord, string(bucketIDs): []byte("legacy-id"), string(bucketLinks): []byte("legacy-link"),
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketRecords, bucketIDs, bucketLinks} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		var schema [8]byte
		binary.BigEndian.PutUint64(schema[:], storeSchemaVersion)
		if err := tx.Bucket(bucketMeta).Put(keySchema, schema[:]); err != nil {
			return err
		}
		for bucket, value := range legacyValues {
			if err := tx.Bucket([]byte(bucket)).Put([]byte("sentinel"), value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if sources, err := store.PendingNotificationSources(10); err != nil || len(sources) != 0 {
		t.Fatalf("migration backfilled history sources=%+v err=%v", sources, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if err := legacy.View(func(tx *bolt.Tx) error {
		schema := tx.Bucket(bucketMeta).Get(keySchema)
		if len(schema) != 8 || binary.BigEndian.Uint64(schema) != storeSchemaVersion {
			t.Fatal("new Core changed the schema version seen by the old Core")
		}
		for bucket, value := range legacyValues {
			if actual := tx.Bucket([]byte(bucket)).Get([]byte("sentinel")); !bytes.Equal(actual, value) {
				t.Fatalf("bucket %s changed", bucket)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEventValidationKeepsReadinessSeparateFromBusinessEvidence(t *testing.T) {
	event := validEvent()
	event.Kind = KindDelivery
	event.Sender, event.Body = "", ""
	event.State, event.CallID = "delivered", "sip-call"
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	event.CallID, event.InReplyTo, event.RPMR = "", "", 0
	if err := event.Validate(); err == nil {
		t.Fatal("uncorrelated delivery report was accepted")
	}
}

func TestStoreDurablyCorrelatesDeliveryAfterProviderGenerationChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	store, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	submitted := validEvent()
	submitted.EventID, submitted.Kind, submitted.MessageID = "old:submitted:message-1:1", KindSubmitted, "message-1"
	submitted.Part, submitted.Sender, submitted.Recipient, submitted.Body = 1, "", "+200", ""
	submitted.CallID = "carrier-call-1"
	if _, _, err := store.Accept(submitted, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	delivery := validEvent()
	delivery.EventID, delivery.Kind, delivery.ProcessGeneration = "new:delivery:report-1:1", KindDelivery, "new"
	delivery.Sender, delivery.Body, delivery.State, delivery.InReplyTo = "", "", "delivered", "carrier-call-1"
	record, _, err := store.Accept(delivery, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if record.MessageID != "message-1" || record.Part != 1 {
		t.Fatalf("correlated delivery=%+v", record)
	}
}

func TestDeliveryCorrelationIsScopedByTransport(t *testing.T) {
	store := openStoreForCellularTest(t)
	now := time.Now().UTC()
	makeSubmitted := func(transport, messageID string) Event {
		event := validEvent()
		event.EventID, event.Kind, event.MessageID = transport+"-submitted", KindSubmitted, messageID
		event.Part, event.Sender, event.Recipient, event.Body = 1, "", "+200", ""
		event.CallID, event.RPMR = "shared-call", 7
		if transport == "cellular" {
			event.ProviderID = "cellular"
		}
		return event
	}
	for _, submitted := range []Event{makeSubmitted("vowifi", "message-vowifi"), makeSubmitted("cellular", "message-cellular")} {
		if _, _, err := store.Accept(submitted, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ transport, messageID string }{{"vowifi", "message-vowifi"}, {"cellular", "message-cellular"}} {
		delivery := validEvent()
		delivery.EventID, delivery.Kind, delivery.Sender, delivery.Body = test.transport+"-delivery", KindDelivery, "", ""
		delivery.State, delivery.InReplyTo, delivery.RPMR = "delivered", "shared-call", 7
		if test.transport == "cellular" {
			delivery.ProviderID = "cellular"
		}
		record, _, err := store.Accept(delivery, now.Add(time.Second))
		if err != nil || record.MessageID != test.messageID {
			t.Fatalf("transport=%s record=%+v err=%v", test.transport, record, err)
		}
	}
}

func TestStoreEnrichesDeliveryThatArrivedBeforeSubmission(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	delivery := validEvent()
	delivery.EventID, delivery.Kind, delivery.Sender, delivery.Body = "delivery:first", KindDelivery, "", ""
	delivery.State, delivery.InReplyTo = "delivered", "carrier-call-late"
	if _, _, err := store.Accept(delivery, time.Now()); err != nil {
		t.Fatal(err)
	}
	submitted := validEvent()
	submitted.EventID, submitted.Kind, submitted.MessageID = "submitted:later", KindSubmitted, "message-late"
	submitted.Part, submitted.Sender, submitted.Recipient, submitted.Body = 1, "", "+200", ""
	submitted.CallID = "carrier-call-late"
	if _, _, err := store.Accept(submitted, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A carrier retransmission remains an exact retry even though the query
	// projection can now enrich it with the later correlation.
	if _, stored, err := store.Accept(delivery, time.Now()); err != nil || stored {
		t.Fatalf("duplicate delivery stored=%v err=%v", stored, err)
	}
	records, err := store.List("line-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	var enriched Record
	for _, record := range records {
		if record.Kind == KindDelivery {
			enriched = record
		}
	}
	if enriched.MessageID != "message-late" || enriched.Part != 1 {
		t.Fatalf("enriched delivery=%+v", enriched)
	}
}

func validEvent() Event {
	return Event{
		SchemaVersion: SchemaVersion, EventID: "sip-call:1", LineID: "line-1",
		ProviderID: "provider-1", ProcessGeneration: "generation-1", Kind: KindReceived,
		ObservedAt: time.Now(), Sender: "+100", Recipient: "+200", Body: "hello",
	}
}
