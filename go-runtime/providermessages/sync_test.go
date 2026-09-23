package providermessages

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestIncrementalSyncBeyondRecentWindowAndDeletedGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	store, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	seed, err := store.Sync("", 50)
	if err != nil || !seed.Initial {
		t.Fatalf("seed=%+v err=%v", seed, err)
	}
	for i := 0; i < 129; i++ {
		event := validEvent()
		event.EventID = fmt.Sprintf("incremental-%d", i)
		if _, _, err = store.Accept(event, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Sync(seed.Cursor, 50)
	if err != nil || first.Initial || first.Gap || !first.More || len(first.Messages) != 50 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	again, err := store.Sync(seed.Cursor, 50)
	if err != nil || again.Cursor != first.Cursor || again.Messages[0].EventID != first.Messages[0].EventID {
		t.Fatal("page not replayable", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Sync(first.Cursor, 50)
	if err != nil || second.Gap || second.Initial || len(second.Messages) != 50 {
		t.Fatal("restart lost cursor", err)
	}
	last, err := store.Sync(second.Cursor, 50)
	if err != nil || last.More || len(last.Messages) != 29 {
		t.Fatalf("last=%+v err=%v", last, err)
	}
	event := validEvent()
	if _, err = store.DeleteHistory(event.LineID, "vowifi", "", []string{"incremental-50"}, false); err != nil {
		t.Fatal(err)
	}
	gap, err := store.Sync(first.Cursor, 50)
	if err != nil || !gap.Gap {
		t.Fatal("deleted history hidden", err)
	}
}

func TestSyncReplacementDatabaseAndBadCursor(t *testing.T) {
	a, err := OpenStore(filepath.Join(t.TempDir(), "a.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenStore(filepath.Join(t.TempDir(), "b.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	first, err := a.Sync("", 50)
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := b.Sync(first.Cursor, 50)
	if err != nil || !replaced.Gap || !replaced.Initial {
		t.Fatal("database reset hidden", err)
	}
	if _, err = b.Sync("broken", 50); err != ErrHistoryQuery {
		t.Fatal("invalid cursor accepted", err)
	}
}
