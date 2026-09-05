package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type readerReadbackStub struct {
	result agentlink.ReaderReadbackResponse
	err    error
	calls  int
}

func (s *readerReadbackStub) ResolveCardRoute(card string) (agentlink.CardRouteTarget, error) {
	return agentlink.CardRouteTarget{Kind: "reader", AgentID: "agent-1", ReaderName: "reader-1", ProcessGeneration: "process-1", SessionGeneration: "session-1", CardID: card}, nil
}

func (s *readerReadbackStub) ReadReader(_ context.Context, _, _ string, req agentlink.ReaderReadbackRequest) (agentlink.ReaderReadbackResponse, error) {
	s.calls++
	if s.err != nil {
		return agentlink.ReaderReadbackResponse{}, s.err
	}
	result := s.result
	result.OperationID, result.ProcessGeneration, result.ReaderName, result.CardID, result.SIMSessionGeneration = req.OperationID, req.ProcessGeneration, req.ReaderName, req.CardID, req.SIMSessionGeneration
	return result, nil
}

func TestReaderReadbackLedgerSuccessAndDuplicate(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &readerReadbackStub{result: agentlink.ReaderReadbackResponse{State: "applied", Reader: &agentlink.ReaderFact{ReaderName: "reader-1", CardID: "89010000000000000001", CardPresent: true, IdentityState: agentlink.CardIdentified}}}
	handler, err := NewReaderReadbackHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"readback-1","process_generation":"process-1","reader_name":"reader-1","card_id":"89010000000000000001","sim_session_generation":"session-1"}`
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload)))
	if first.Code != http.StatusOK || stub.calls != 1 {
		t.Fatalf("first status=%d calls=%d body=%s", first.Code, stub.calls, first.Body.String())
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload)))
	if second.Code != http.StatusOK || stub.calls != 1 {
		t.Fatalf("duplicate status=%d calls=%d body=%s", second.Code, stub.calls, second.Body.String())
	}
	receipt, found, err := store.GetOperation("readback-1")
	if err != nil || !found || receipt.State != linecatalog.OperationSucceeded {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestReaderReadbackRecordsUnknownAndRejectsTargetMismatch(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stub := &readerReadbackStub{err: context.DeadlineExceeded}
	handler, err := NewReaderReadbackHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"operation_id":"readback-unknown","process_generation":"process-1","reader_name":"reader-1","card_id":"89010000000000000001","sim_session_generation":"session-1"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("unknown status=%d body=%s", response.Code, response.Body.String())
	}
	receipt, found, err := store.GetOperation("readback-unknown")
	if err != nil || !found || receipt.State != linecatalog.OperationUnknown {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
	mismatch := httptest.NewRecorder()
	handler.ServeHTTP(mismatch, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Replace(payload, "reader-1", "reader-2", 1))))
	if mismatch.Code != http.StatusConflict || stub.calls != 1 {
		t.Fatalf("mismatch status=%d calls=%d body=%s", mismatch.Code, stub.calls, mismatch.Body.String())
	}
}
