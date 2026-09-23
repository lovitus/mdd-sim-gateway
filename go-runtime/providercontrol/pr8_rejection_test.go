package providercontrol

import (
	"bytes"
	"encoding/json"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPR8RecoveryReadsRejectedOutcomeAndKeepsCapabilityFence(t *testing.T) {
	store, err := callhistory.Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := strings.Repeat("a", 64)
	record := callhistory.RecoveryRecord{LineID: "line-1", Transport: "vowifi", CardID: "8944100000000000001", CallID: "original-call", OperationID: "original-start", SessionID: "original-session", Subject: "original-login", ProviderID: "provider-1", ProviderGeneration: "generation-1", CreatedAt: time.Now().Add(-time.Minute)}
	if err = store.BindRecovery(record, key); err != nil {
		t.Fatal(err)
	}
	backend := &receiptBackend{fakeBackend: newFakeBackend("generation-2"), receipt: vowifiipc.CallReceipt{CallReceiptRequest: vowifiipc.CallReceiptRequest{CallID: record.CallID, OperationID: record.OperationID, SessionID: record.SessionID}, LineID: record.LineID, ProviderID: record.ProviderID, ProcessGeneration: record.ProviderGeneration, ConfirmedAt: time.Now(), Source: "confirmed_rejected"}}
	api, err := vowifiipc.NewAPI(backend, testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(api)
	defer provider.Close()
	directory := mediaauth.NewProviderDirectory()
	if err = directory.Replace(mediaauth.Provider{LineID: record.LineID, ProviderID: record.ProviderID, Generation: "generation-2", CardID: record.CardID, BaseURL: "ws" + strings.TrimPrefix(provider.URL, "http"), Token: testToken}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(directory, paidActionCatalog(), provider.Client(), WithCallRecorder(store))
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(capability string) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]string{"action": "status", "call_id": record.CallID, "operation_id": record.OperationID, "recovery_key": capability})
		r := httptest.NewRequest("POST", "/v1/lines/line-1/vowifi/calls/recovery", bytes.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("lineID", record.LineID)
		r.SetPathValue("operation", "calls/recovery")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if response := invoke(strings.Repeat("b", 64)); response.Code != 404 || backend.reads != 0 {
		t.Fatal("wrong grant queried provider")
	}
	response := invoke(key)
	var result map[string]any
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result["terminal_confirmed"] != true || result["outcome"] != "rejected" || backend.reads != 1 {
		t.Fatalf("%d %s reads=%d", response.Code, response.Body.String(), backend.reads)
	}
	if len(backend.operations) != 0 {
		t.Fatal("natural terminal lookup sent a mutation")
	}
	// A subsequent read is served by the persisted Core evidence, not an End request.
	if response = invoke(key); response.Code != 200 || !strings.Contains(response.Body.String(), `"outcome":"rejected"`) || backend.reads != 1 {
		t.Fatal("stored terminal re-queried provider")
	}
}
