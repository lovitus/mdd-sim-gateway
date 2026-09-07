package providermessages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestConversationHistoryKeepsOlderPeersAndStablePages(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Unix(1800100000, 0)
	for i := 0; i < 105; i++ {
		event := validEvent()
		event.EventID = fmt.Sprintf("history-%d", i)
		event.Sender = "older-peer"
		if i > 0 {
			event.Sender = "recent-peer"
		}
		event.ObservedAt = at.Add(time.Duration(i) * time.Second)
		if _, _, err := store.Accept(event, event.ObservedAt); err != nil {
			t.Fatal(err)
		}
	}
	event := validEvent()
	threads, err := store.Conversations(event.LineID, "vowifi")
	if err != nil || len(threads) != 2 || threads[0].Count != 104 || threads[1].Peer != "older-peer" {
		t.Fatalf("threads=%+v err=%v", threads, err)
	}
	first, err := store.MessagePage(event.LineID, "vowifi", "recent-peer", "", 50)
	if err != nil || len(first.Messages) != 50 || first.NextBefore == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := store.MessagePage(event.LineID, "vowifi", "recent-peer", first.NextBefore, 50)
	if err != nil || len(second.Messages) != 50 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	newEvent := validEvent()
	newEvent.EventID, newEvent.Sender = "new-arrival", "recent-peer"
	if _, _, err := store.Accept(newEvent, at.Add(200*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteHistory(event.LineID, "vowifi", "", []string{first.Messages[0].EventID}, false); err != nil {
		t.Fatal(err)
	}
	again, err := store.MessagePage(event.LineID, "vowifi", "recent-peer", first.NextBefore, 50)
	if err != nil || len(again.Messages) != len(second.Messages) {
		t.Fatalf("mutated page=%+v %v", again, err)
	}
	for i := range again.Messages {
		if again.Messages[i].EventID != second.Messages[i].EventID {
			t.Fatal("arrival/deletion shifted cursor")
		}
	}
	seen := make(map[string]bool)
	for _, record := range first.Messages {
		seen[record.EventID] = true
	}
	for _, record := range second.Messages {
		if seen[record.EventID] {
			t.Fatal("cursor duplicated record")
		}
	}
	last, err := store.MessagePage(event.LineID, "vowifi", "recent-peer", second.NextBefore, 50)
	if err != nil || len(last.Messages) != 4 || last.NextBefore != "" {
		t.Fatalf("last=%+v err=%v", last, err)
	}
	if _, err := store.MessagePage(event.LineID, "vowifi", "recent-peer", "invalid", 50); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	if _, err := store.Conversations("", "vowifi"); err == nil {
		t.Fatal("unscoped conversation accepted")
	}
}

func TestHistoryHTTPDistinguishesBadQueryAndStoreFailure(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler, err := NewPublicHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/v1/messages?page=true&line_id=line-1&transport=vowifi&before=bad",
		"/v1/messages?page=true&line_id=line-1&transport=vowifi&limit=501",
		"/v1/messages?page=true&line_id=line-1&transport=vowifi&peer=a&peer=b",
		"/v1/messages/conversations?line_id=line-1",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", path, response.Code)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/v1/messages?page=true&line_id=line-1&transport=vowifi",
		"/v1/messages/conversations?line_id=line-1&transport=vowifi",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("DB failure became %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestPeerlessDeliveryFollowsExactConversationAndDeletion(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	submitted := validEvent()
	submitted.Kind, submitted.EventID, submitted.MessageID = KindSubmitted, "submitted-history", "message-history"
	submitted.Sender, submitted.Recipient = "", "recipient-a"
	submitted.Part, submitted.CallID = 1, "sip-history"
	if _, _, err := store.Accept(submitted, submitted.ObservedAt); err != nil {
		t.Fatal(err)
	}
	report := submitted
	report.EventID, report.Kind, report.MessageID = "delivery-history", KindDelivery, ""
	report.Sender, report.Recipient, report.Body, report.State = "", "", "", "delivered"
	if _, _, err := store.Accept(report, report.ObservedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	threads, err := store.Conversations(submitted.LineID, "vowifi")
	if err != nil || len(threads) != 1 || threads[0].Count != 2 {
		t.Fatalf("delivery lost: %+v %v", threads, err)
	}
	page, err := store.MessagePage(submitted.LineID, "vowifi", "recipient-a", "", 100)
	if err != nil || len(page.Messages) != 2 || page.Messages[1].Kind != KindDelivery {
		t.Fatalf("page=%+v %v", page, err)
	}
	deleted, err := store.DeleteHistory(submitted.LineID, "vowifi", "recipient-a", nil, false)
	if err != nil || deleted != 2 {
		t.Fatalf("delete=%d %v", deleted, err)
	}
	if _, stored, err := store.Accept(report, report.ObservedAt.Add(2*time.Second)); err != nil || stored {
		t.Fatalf("deleted report replayed: %v %v", stored, err)
	}
}
