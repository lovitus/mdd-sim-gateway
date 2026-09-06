package cellularmessages

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPurgeLineRemovesOnlyMatchingOperations(t *testing.T) {
	store, err := OpenOperationStore(filepath.Join(t.TempDir(), "operations.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, lineID := range []string{"line-delete", "line-keep"} {
		record := OperationRecord{OperationID: "operation-" + lineID, MessageID: "message-" + lineID,
			LineID: lineID, EquipmentID: "123456789012345", CardID: "8944100000000000001",
			AgentID: "agent-1", ProcessGeneration: "process-1", AttachmentID: "attachment-1",
			Recipient: "+44123", BodySHA256: "digest", CreatedAt: time.Now()}
		if _, created, err := store.Begin(record); err != nil || !created {
			t.Fatalf("begin %s created=%t err=%v", lineID, created, err)
		}
	}
	if err := store.PurgeLine("line-delete"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get("operation-line-delete"); found {
		t.Fatal("matching operation remains")
	}
	if _, found, _ := store.Get("operation-line-keep"); !found {
		t.Fatal("unrelated operation was removed")
	}
	late := OperationRecord{OperationID: "operation-late", MessageID: "message-late", LineID: "line-delete",
		EquipmentID: "123456789012345", CardID: "8944100000000000001", AgentID: "agent-1",
		ProcessGeneration: "process-1", AttachmentID: "attachment-1", Recipient: "+44123",
		BodySHA256: "digest", CreatedAt: time.Now()}
	if _, _, err := store.Begin(late); err == nil {
		t.Fatal("purged line accepted a late SMS operation")
	}
}
