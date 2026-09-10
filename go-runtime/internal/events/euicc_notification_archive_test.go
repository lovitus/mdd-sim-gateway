package events

import (
	"bytes"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"path/filepath"
	"testing"
	"time"
)

func TestDeletionNotificationArchiveAndReplayAreDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	eid := "89049032000000000000000000000001"
	entry := agentlink.EUICCNotificationEntry{ICCID: "8944000000000000001", Event: "delete", SequenceNumber: 0, Address: "notify.example.com"}
	payload := []byte("fixture-only-not-a-real-signed-notification")
	a, err := s.SaveEUICCNotification(eid, entry, payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveEUICCNotification(eid, entry, []byte("replacement")); err == nil {
		t.Fatal("overwrote signed material")
	}
	if _, fresh, err := s.BeginEUICCReplay(eid, 0, "attempt-1", a.SHA256); err != nil || !fresh {
		t.Fatal(err)
	}
	if _, fresh, err := s.BeginEUICCReplay(eid, 0, "attempt-1", a.SHA256); err != nil || fresh {
		t.Fatal("duplicate dispatch allowed")
	}
	if err = s.FinishEUICCReplay(eid, 0, "attempt-1", "acknowledged"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err = s.EUICCNotificationArchive(eid, 0)
	if err != nil || !bytes.Equal(a.Payload, payload) || a.Attempts[0].State != "acknowledged" {
		t.Fatal("archive did not survive reopen/ack")
	}
	all, err := s.EUICCNotificationArchives(eid)
	if err != nil || len(all) != 1 || len(all[0].Payload) != 0 {
		t.Fatal("public archive list exposes payload")
	}
}
