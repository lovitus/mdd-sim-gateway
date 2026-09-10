package events

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStandardDeletionIntentSurvivesRestartWithoutRedispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r := EUICCDeletion{EID: "89049032000000000000000000000001", ICCID: "8944000000000000001", OperationID: "delete-1", BeforeSequences: []int64{1}}
	if _, fresh, err := s.BeginEUICCDeletion(r); err != nil || !fresh {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, fresh, err := s.BeginEUICCDeletion(r); err != nil || fresh || got.State != "pending" {
		t.Fatal("pending deletion would redispatch", err)
	}
	other := r
	other.OperationID = "delete-2"
	if _, _, err := s.BeginEUICCDeletion(other); err == nil {
		t.Fatal("unresolved deletion overwritten")
	}
	if _, err := s.FinishEUICCDeletion(r.EID, r.OperationID, "deleted_observed", "readback"); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachEUICCDeletionNotifications(r.EID, r.OperationID, []int64{2}); err != nil {
		t.Fatal(err)
	}
	if got, found, err := s.EUICCDeletion(r.EID, r.OperationID); err != nil || !found || len(got.Notifications) != 1 || got.Notifications[0] != 2 {
		t.Fatal("notification reference lost", err)
	}
	if got, err := s.FinishEUICCDeletion(r.EID, r.OperationID, "unknown", "late transport error"); err != nil || got.State != "deleted_observed" {
		t.Fatal("known deletion regressed")
	}
}
