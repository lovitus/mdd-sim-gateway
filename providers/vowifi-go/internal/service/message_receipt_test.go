// SPDX-License-Identifier: AGPL-3.0-only
package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestMessageReceiptReadsExactDurableOperationWithoutSending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{}
	backend, err := NewBackendWithMediaStore("line-1", "native", "old", &fakeFactory{run: runtime}, store, nil, time.Second, "8944100000000000001")
	if err != nil {
		t.Fatal(err)
	}
	request := vowifiipc.SendMessageRequest{OperationID: "operation-a", MessageID: "message-a", Recipient: "+15550100123", Body: "original body"}
	if _, err := backend.MessageReceipt(t.Context(), request); operationCode(err) != "message_receipt_not_found" {
		t.Fatalf("missing=%v", err)
	}
	if _, found, err := store.LookupMessage(request.OperationID); err != nil || found {
		t.Fatal("receipt query reserved a send")
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.SendMessage(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "stop"}); err != nil {
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
	factory := &fakeFactory{}
	reopened, err := NewBackendWithMediaStore("line-1", "native", "new", factory, store, nil, time.Second, "8944100000000000001")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := reopened.MessageReceipt(t.Context(), request)
	if err != nil || receipt.MessageID != request.MessageID || receipt.OperationID != request.OperationID || receipt.Code != "sent" || receipt.Status.ProcessGeneration != "new" || runtime.messages.Load() != 1 || factory.starts.Load() != 0 {
		t.Fatalf("receipt=%+v err=%v sends=%d starts=%d", receipt, err, runtime.messages.Load(), factory.starts.Load())
	}
	changed := request
	changed.Body = "different"
	if _, err := reopened.MessageReceipt(t.Context(), changed); operationCode(err) != "operation_id_reused" {
		t.Fatalf("changed body=%v", err)
	}
	changed = request
	changed.Recipient = "+15550100999"
	if _, err := reopened.MessageReceipt(t.Context(), changed); operationCode(err) != "operation_id_reused" {
		t.Fatalf("changed recipient=%v", err)
	}
	other, _ := NewBackendWithMediaStore("line-1", "native", "other", factory, store, nil, time.Second, "8944100000000000002")
	if _, err := other.MessageReceipt(t.Context(), request); operationCode(err) != "operation_id_reused" {
		t.Fatalf("other SIM=%v", err)
	}
	pending := request
	pending.OperationID = "pending"
	if err := store.ReserveMessage(pending.OperationID, MessageOperationRecord{OperationRecord: OperationRecord{Kind: operationKind("message_send", pending.MessageID, pending.Recipient, pending.Body)}, Scope: reopened.messageScope, Generation: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.MessageReceipt(t.Context(), pending); operationCode(err) != "operation_result_unknown" {
		t.Fatalf("pending=%v", err)
	}
	if runtime.messages.Load() != 1 || factory.starts.Load() != 0 {
		t.Fatal("query changed runtime or resent SMS")
	}
}
