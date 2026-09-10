package events

import (
	"path/filepath"
	"testing"
	"time"
)

func TestEUICCDeletionEventPersistsWithoutCarrierAcknowledgement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	store, err := OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	event := EUICCSoftDeleteEvent{EID: "89049032000000000000000000000001", ICCID: "8944000000000000001", OperationID: "delete-test", OriginalNickname: "original", Marker: "[MDD-DELETED]"}
	first, err := store.BeginEUICCSoftDelete(event)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.BeginEUICCSoftDelete(event)
	if err != nil || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatal("duplicate changed event")
	}
	if _, err = store.FinishEUICCSoftDelete(event.EID, event.ICCID, "other", "marked"); err == nil {
		t.Fatal("wrong operation accepted")
	}
	if _, err = store.FinishEUICCSoftDelete(event.EID, event.ICCID, event.OperationID, "marked"); err != nil {
		t.Fatal(err)
	}
	replay, err := NewReplay(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	purger, err := NewLinePurger(store, replay)
	if err != nil {
		t.Fatal(err)
	}
	if err = purger.PurgeLine("unrelated-line"); err != nil {
		t.Fatal(err)
	}
	second := event
	second.OperationID = "second-delete"
	second.OriginalNickname = "manually restored"
	if _, err = store.BeginEUICCSoftDelete(second); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginEUICCSoftDelete(event); err == nil {
		t.Fatal("historical operation reused")
	}
	if _, err = store.FinishEUICCSoftDelete(second.EID, second.ICCID, second.OperationID, "marked"); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, found, err := store.EUICCSoftDelete(event.EID, event.ICCID)
	if err != nil || !found || got.State != "marked" || got.NotificationState != "signed_delete_notification_unavailable" || got.OriginalNickname != "manually restored" {
		t.Fatalf("event lost or carrier result invented: %+v %v", got, err)
	}
	history, err := store.EUICCSoftDeleteHistory(event.EID, event.ICCID)
	if err != nil || len(history) != 2 || history[0].OriginalNickname != "original" || history[1].OperationID != "second-delete" {
		t.Fatalf("deletion history lost: %+v %v", history, err)
	}
}
