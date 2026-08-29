package agentsim

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func TestDownloadStorePersistsOnlySecretFreeReplayFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downloads.db")
	store, err := OpenDownloadStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10, 0).UTC()
	oneUseRequest := agentlink.EUICCDownloadRequest{
		OperationID: "download-1", SessionGeneration: "insertion-1", EID: testEID,
		Action: agentlink.EUICCDownloadStart, ActivationCode: "LPA:1$secret.example$secret-matching-id",
		ConfirmationCode: "secret-confirmation", IMEI: "123456789012345",
	}
	digest := downloadRequestDigest(oneUseRequest)
	record := DownloadRecord{
		OperationID: "download-1", EID: testEID,
		Job: agentlink.EUICCDownloadJob{
			State: agentlink.EUICCDownloadQueued, Stage: agentlink.EUICCDownloadStageQueued,
			StartedAt: now, UpdatedAt: now,
		},
	}
	created, inserted, err := store.Begin(record)
	if err != nil || !inserted || created.OperationID != record.OperationID {
		t.Fatalf("Begin() record=%+v inserted=%t err=%v", created, inserted, err)
	}
	replayed, inserted, err := store.Begin(record)
	if err != nil || inserted || replayed.OperationID != record.OperationID {
		t.Fatalf("replay record=%+v inserted=%t err=%v", replayed, inserted, err)
	}
	conflict := record
	conflict.EID = "89049032000000000000000000000002"
	if _, _, err := store.Begin(conflict); err != ErrDownloadConflict {
		t.Fatalf("conflicting replay err=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret.example", "secret-matching-id", "secret-confirmation", "123456789012345", digest} {
		if bytes.Contains(wire, []byte(secret)) {
			t.Fatalf("persisted one-use secret %q", secret)
		}
	}
}

func TestDownloadStoreMarksInterruptedWorkUncertain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downloads.db")
	store, err := OpenDownloadStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10, 0).UTC()
	_, _, err = store.Begin(DownloadRecord{
		OperationID: "download-running", EID: testEID,
		Job: agentlink.EUICCDownloadJob{
			State: agentlink.EUICCDownloadRunning, Stage: agentlink.EUICCDownloadStageInstall,
			StartedAt: now, UpdatedAt: now,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveredAt := time.Unix(20, 0).UTC()
	if err := store.RecoverInterrupted(recoveredAt); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Get("download-running")
	if err != nil || !found || record.Job.State != agentlink.EUICCDownloadUncertain ||
		record.Job.Code != "agent_runtime_interrupted" || !record.Job.UpdatedAt.Equal(recoveredAt) {
		t.Fatalf("record=%+v found=%t err=%v", record, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
