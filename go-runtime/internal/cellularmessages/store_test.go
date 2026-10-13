package cellularmessages

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

func deletionSMS(line string) OperationRecord {
	return OperationRecord{OperationID: "operation-" + line, MessageID: "message-" + line,
		LineID: line, EquipmentID: "123456789012345", CardID: "8944100000000000001",
		AgentID: "agent-1", ProcessGeneration: "process-1", AttachmentID: "attachment-1",
		Recipient: "+44123", BodySHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("hello"))), CreatedAt: time.Now().UTC()}
}

func persistDeletionSMS(t *testing.T, history *providermessages.Store, record OperationRecord, body string) {
	t.Helper()
	for index, reference := range record.References {
		_, stored, err := history.Accept(providermessages.Event{SchemaVersion: 1,
			EventID: "cellular-submit-" + record.OperationID + "-" + stringID(index+1), LineID: record.LineID,
			ProviderID: "cellular", ProcessGeneration: record.ProcessGeneration, Kind: providermessages.KindSubmitted,
			ObservedAt: record.CreatedAt, MessageID: record.MessageID, Part: index + 1, Recipient: record.Recipient,
			CallID: messageReferenceID(reference), RPMR: reference, State: "submitted", Body: body}, record.CreatedAt)
		if err != nil || !stored {
			t.Fatalf("history stored=%t err=%v", stored, err)
		}
	}
}

func TestDeletionBarrierRequiresResolvedSMSAndExactDurableHistory(t *testing.T) {
	for _, state := range []string{"prepared", "uncertain", "unrecognized", "submitted-no-history", "submitted-wrong-body", "submitted"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			store, err := OpenOperationStore(filepath.Join(root, "operations.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			history, err := providermessages.OpenStore(filepath.Join(root, "messages.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer history.Close()
			record := deletionSMS("line-delete")
			if _, _, err := store.Begin(record); err != nil {
				t.Fatal(err)
			}
			if state != "prepared" {
				mark := state
				if state == "submitted-no-history" || state == "submitted-wrong-body" {
					mark = "submitted"
				}
				record, err = store.Mark(record.OperationID, mark, []int{9, 10})
				if err != nil {
					t.Fatal(err)
				}
			}
			if state == "submitted" || state == "submitted-wrong-body" {
				body := "hello"
				if state == "submitted-wrong-body" {
					body = "different content"
				}
				persistDeletionSMS(t, history, record, body)
			}
			err = store.PrepareLineDeletion(record.LineID, history)
			if state == "submitted" {
				if err != nil {
					t.Fatal(err)
				}
				if err := store.RetainLine(record.LineID); err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, ErrUnresolvedOperation) {
					t.Fatalf("unresolved record accepted: %v", err)
				}
				if err := store.PurgeLine(record.LineID); err == nil {
					t.Fatal("purge bypassed barrier")
				}
			}
			if _, found, err := store.Get(record.OperationID); err != nil || !found {
				t.Fatal("evidence lost", err)
			}
		})
	}
}

type heldSubmissionHistory struct {
	*providermessages.Store
	entered, release chan struct{}
}

func (history *heldSubmissionHistory) Find(line, provider, event string) (providermessages.Record, bool, error) {
	close(history.entered)
	<-history.release
	return history.Store.Find(line, provider, event)
}

func TestDeletionBarrierSerializesWithBeginAndSurvivesBackup(t *testing.T) {
	root := t.TempDir()
	store, err := OpenOperationStore(filepath.Join(root, "operations.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	history, err := providermessages.OpenStore(filepath.Join(root, "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	before := deletionSMS("begin-first")
	if _, _, err := store.Begin(before); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareLineDeletion(before.LineID, history); !errors.Is(err, ErrUnresolvedOperation) {
		t.Fatalf("earlier Begin must block deletion: %v", err)
	}
	record := deletionSMS("delete-first")
	if _, _, err := store.Begin(record); err != nil {
		t.Fatal(err)
	}
	record, err = store.Mark(record.OperationID, "submitted", []int{7})
	if err != nil {
		t.Fatal(err)
	}
	persistDeletionSMS(t, history, record, "hello")
	held := &heldSubmissionHistory{Store: history, entered: make(chan struct{}), release: make(chan struct{})}
	deletionDone, beginDone := make(chan error, 1), make(chan error, 1)
	go func() { deletionDone <- store.PrepareLineDeletion(record.LineID, held) }()
	<-held.entered
	late := record
	late.OperationID, late.MessageID = "late-operation", "late-message"
	go func() { _, _, err := store.Begin(late); beginDone <- err }()
	close(held.release)
	if err := <-deletionDone; err != nil {
		t.Fatal(err)
	}
	if err := <-beginDone; err == nil {
		t.Fatal("Begin crossed a deletion transaction")
	}
	if err := store.RetainLine(record.LineID); err != nil {
		t.Fatal(err)
	}
	wire, err := store.Backup()
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(root, "restored.db")
	if err := os.WriteFile(backupPath, wire, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenOperationStore(backupPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, found, err := restored.Get(record.OperationID); err != nil || !found {
		t.Fatal("retained SMS absent in backup", err)
	}
	if _, _, err := restored.Begin(late); err == nil {
		t.Fatal("backup lost deletion barrier")
	}
	if err := store.PurgeLine(record.LineID); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(record.OperationID); found {
		t.Fatal("matching operation remains after explicit purge")
	}
	if _, found, _ := store.Get(before.OperationID); !found {
		t.Fatal("unrelated operation was purged")
	}
	if err := history.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareLineDeletion(record.LineID, history); err != nil {
		t.Fatal("durable barrier retry should not need deleted history", err)
	}
	other := deletionSMS("history-unavailable")
	if _, _, err := store.Begin(other); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Mark(other.OperationID, "submitted", []int{8}); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareLineDeletion(other.LineID, history); err == nil {
		t.Fatal("unavailable history accepted")
	}
}
