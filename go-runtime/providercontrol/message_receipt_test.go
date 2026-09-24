package providercontrol

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type receiptMessageBackend struct {
	*fakeBackend
	missing atomic.Bool
}

func (backend *receiptMessageBackend) MessageReceipt(_ context.Context, request vowifiipc.SendMessageRequest) (vowifiipc.MessageResult, error) {
	backend.record("messages/receipt")
	if backend.missing.Load() {
		return vowifiipc.MessageResult{}, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotFound, Code: "message_receipt_not_found", Layer: "messaging"}
	}
	return vowifiipc.MessageResult{OperationResult: backend.operation(request.OperationID, "sent"), MessageID: request.MessageID}, nil
}

func TestMessageReceiptWorksWithoutLiveSIMOrEnabledIntentButCannotSend(t *testing.T) {
	backend := &receiptMessageBackend{fakeBackend: newFakeBackend("generation-1")}
	provider := providerServer(t, backend)
	directory := mediaauth.NewProviderDirectory()
	registerProvider(t, directory, provider.URL, "generation-1")
	catalog := paidActionCatalog()
	catalog.line.Enabled = false
	handler, err := NewHandler(directory, catalog, provider.Client()) // Deliberately no live card resolver.
	if err != nil {
		t.Fatal(err)
	}
	public := publicServer(handler)
	defer public.Close()
	body := `{"operation_id":"original","message_id":"message","recipient":"+15550100123","body":"original","expected_card_id":"8944100000000000001"}`
	result := postJSON(t, public.URL+"/v1/lines/line-1/vowifi/messages/receipt", body)
	if result.status != http.StatusOK {
		t.Fatalf("receipt=%d %s", result.status, result.body)
	}
	backend.missing.Store(true)
	result = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/messages/receipt", body)
	assertFailure(t, result, http.StatusNotFound, "message_receipt_not_found")
	result = postJSON(t, public.URL+"/v1/lines/line-1/vowifi/messages/receipt", strings.ReplaceAll(body, "8944100000000000001", "8944100000000000002"))
	assertFailure(t, result, http.StatusConflict, "paid_action_card_mismatch")
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if strings.Join(backend.operations, ",") != "messages/receipt,messages/receipt" {
		t.Fatalf("operations=%v", backend.operations)
	}
}
