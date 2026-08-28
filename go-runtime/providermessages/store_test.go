package providermessages

import (
	"path/filepath"
	"testing"
	"time"
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
