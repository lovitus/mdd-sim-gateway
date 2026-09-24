package callhistory

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryAssociationSurvivesRestartAndRequiresOriginalCapability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	key := strings.Repeat("a", 64)
	record := RecoveryRecord{LineID: "line-1", Transport: "vowifi", CardID: "8944100000000000001", CallID: "call-1", OperationID: "start-1", SessionID: "session-1", Subject: "subject-1", ProviderID: "provider-1", ProviderGeneration: "generation-1", CreatedAt: now}
	if err = store.BindRecovery(record, key); err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateRecoveryDispatch(record.LineID, record.Transport, record.CallID, record.OperationID, record.SessionID, record.Subject, record.CardID, record.ProviderGeneration); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"call", "operation", "session", "subject", "generation"} {
		call, operation, session, subject, generation := record.CallID, record.OperationID, record.SessionID, record.Subject, record.ProviderGeneration
		switch scenario {
		case "call":
			call = "another-call"
		case "operation":
			operation = "another-operation"
		case "session":
			session = "another-session"
		case "subject":
			subject = "another-subject"
		case "generation":
			generation = "another-generation"
		}
		if err = store.ValidateRecoveryDispatch(record.LineID, record.Transport, call, operation, session, subject, record.CardID, generation); !errors.Is(err, ErrRecoveryIdentity) {
			t.Fatalf("%s bypassed original lease binding: %v", scenario, err)
		}
	}
	if err = store.Start(record.LineID, record.Transport, record.CallID, "out", "", now); err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(record.LineID, record.Transport, record.CallID, "ended", now); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.ReadRecovery(record.LineID, record.Transport, record.CallID, record.OperationID, key)
	if err != nil || !restored.TerminalAt.IsZero() {
		t.Fatal("display history became terminal evidence", err)
	}
	if _, err = store.ReadRecovery(record.LineID, record.Transport, record.CallID, record.OperationID, strings.Repeat("b", 64)); !errors.Is(err, ErrRecoveryIdentity) {
		t.Fatal("wrong capability accepted", err)
	}
	if _, err = store.ReadRecovery(record.LineID, record.Transport, record.CallID, "other-operation", key); !errors.Is(err, ErrRecoveryIdentity) {
		t.Fatal("wrong operation accepted", err)
	}
	if err = store.ConfirmRecovery(restored, now, "agent_terminal_receipt"); !errors.Is(err, ErrRecoveryIdentity) {
		t.Fatal("wrong transport proof accepted", err)
	}
	if err = store.ConfirmRecovery(restored, now, "provider_terminal_receipt"); err != nil {
		t.Fatal(err)
	}
	confirmed, err := store.ReadRecovery(record.LineID, record.Transport, record.CallID, record.OperationID, key)
	if err != nil || confirmed.TerminalAt.IsZero() {
		t.Fatal("exact terminal lost", err)
	}
	if err = store.ConfirmRecoveryOutcome(confirmed, now.Add(time.Second), "provider_terminal_receipt", "rejected"); err != nil {
		t.Fatal("provider rejection outcome was not accepted", err)
	}
	confirmed, err = store.ReadRecovery(record.LineID, record.Transport, record.CallID, record.OperationID, key)
	if err != nil || confirmed.TerminalOutcome != "ended" {
		t.Fatal("a second proof changed the first durable outcome", err, confirmed.TerminalOutcome)
	}
	record.Subject = "replacement-session"
	if err = store.BindRecovery(record, key); !errors.Is(err, ErrRecoveryIdentity) {
		t.Fatal("record owner replaced", err)
	}
}
