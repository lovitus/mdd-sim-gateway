// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestBoltOperationStoreSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve("generation-1", "start-1", "start"); err != nil {
		t.Fatal(err)
	}
	result := vowifiipc.OperationResult{OperationID: "start-1", Accepted: true, Code: "started", Status: validStoreSnapshot()}
	if err := store.Complete("generation-1", "start-1", result); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, found, err := store.Lookup("generation-1", "start-1")
	if err != nil || !found || !record.Done || record.Result.Code != "started" {
		t.Fatalf("record=%+v found=%v err=%v", record, found, err)
	}
	if _, found, err := store.Lookup("generation-2", "start-1"); err != nil || found {
		t.Fatalf("new generation found=%v err=%v", found, err)
	}
}

func TestBoltOperationStorePersistsAndAdoptsMessageOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	event := providermessages.Event{
		SchemaVersion: providermessages.SchemaVersion, EventID: "old:received:call:1",
		LineID: "line-1", ProviderID: "provider-1", ProcessGeneration: "old",
		Kind: providermessages.KindReceived, ObservedAt: time.Now(), Sender: "+100", Body: "hello",
	}
	if err := store.EnqueueMessage(event); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AdoptMessages("line-1", "provider-1", "new"); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingMessages(10)
	if err != nil || len(pending) != 1 || pending[0].ProcessGeneration != "new" || pending[0].EventID != event.EventID {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := store.DeleteMessage(pending[0]); err != nil {
		t.Fatal(err)
	}
	if pending, err = store.PendingMessages(10); err != nil || len(pending) != 0 {
		t.Fatalf("pending after delete=%+v err=%v", pending, err)
	}
}

func TestBoltOperationStoreRepairsFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o err=%v", info.Mode().Perm(), err)
	}
}

func validStoreSnapshot() vowifiipc.Snapshot {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	return vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-1", ProviderID: "native",
		ProcessGeneration: "generation-1", Sequence: 2,
		ObservedAt: time.Now().UTC(), Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning, Code: "ready"},
		Tunnel: ready, IMS: ready, Voice: ready, Messaging: ready,
	}
}
