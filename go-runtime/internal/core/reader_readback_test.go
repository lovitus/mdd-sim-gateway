package core

import (
	"context"
	"encoding/json"
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
	result.Scope = req.Scope
	result.OperationID, result.ProcessGeneration, result.ReaderName, result.CardID, result.SIMSessionGeneration = req.OperationID, req.ProcessGeneration, req.ReaderName, req.CardID, req.SIMSessionGeneration
	if result.Reader != nil {
		result.Reader.ReaderName, result.Reader.CardID = req.ReaderName, req.CardID
		result.Reader.SessionGeneration = req.SIMSessionGeneration
	}
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

func TestReaderProvisionPromotesOnlyMatchingDisabledDraft(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1", Name: "reader line",
		CardID: "89010000000000000001", HardwareProvisionState: "draft",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", SMSC: "+447785016005"}}
	if _, revision, err := store.PutExpected(line, 1); err != nil || revision != 2 {
		t.Fatalf("create draft revision=%d error=%v", revision, err)
	}
	stub := &readerReadbackStub{result: agentlink.ReaderReadbackResponse{State: "applied",
		Reader: &agentlink.ReaderFact{ReaderName: "reader-1", CardID: line.CardID, CardPresent: true,
			SessionGeneration: "session-1", IdentityState: agentlink.CardIdentified,
			SIM: &agentlink.ReaderSIMFact{IdentityState: "ready", IMSI: "234100000000001", MCC: "234", MNC: "10"}}}}
	handler, err := NewReaderProvisionHandler(stub, store)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"schema_version":1,"operation_id":"reader-provision-1","line_id":"line-1","expected_catalog_revision":2,"process_generation":"process-1","reader_name":"reader-1","card_id":"89010000000000000001","sim_session_generation":"session-1"}`
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload)))
	if first.Code != http.StatusOK || stub.calls != 1 {
		t.Fatalf("first status=%d calls=%d body=%s", first.Code, stub.calls, first.Body.String())
	}
	stored, revision, err := store.GetWithRevision("line-1")
	if err != nil || revision != 3 || stored.Enabled || stored.HardwareProvisionState != "provisioned" {
		t.Fatalf("stored=%+v revision=%d error=%v", stored, revision, err)
	}
	receipt, found, err := store.GetOperation("reader-provision-1")
	if err != nil || !found || receipt.Kind != linecatalog.OperationReaderProvision || receipt.State != linecatalog.OperationSucceeded ||
		receipt.ReaderName != "reader-1" || receipt.SIMSessionGeneration != "session-1" ||
		receipt.OutcomeCode != "reader_provision_identity_verified" {
		t.Fatalf("receipt=%+v found=%v error=%v", receipt, found, err)
	}
	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload)))
	if duplicate.Code != http.StatusOK || stub.calls != 1 {
		t.Fatalf("duplicate status=%d calls=%d body=%s", duplicate.Code, stub.calls, duplicate.Body.String())
	}
}

func TestReaderProvisionRejectsStaleCatalogBeforeAgentReadback(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1",
		CardID: "89010000000000000001", HardwareProvisionState: "draft",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", SMSC: "+447785016005"}}
	if _, _, err := store.PutExpected(line, 1); err != nil {
		t.Fatal(err)
	}
	stub := &readerReadbackStub{}
	handler, _ := NewReaderProvisionHandler(stub, store)
	payload := `{"schema_version":1,"operation_id":"reader-provision-stale","line_id":"line-1","expected_catalog_revision":3,"process_generation":"process-1","reader_name":"reader-1","card_id":"89010000000000000001","sim_session_generation":"session-1"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload)))
	if response.Code != http.StatusConflict || stub.calls != 0 ||
		!strings.Contains(response.Body.String(), "reader_provision_requires_disabled_draft") {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
}

func TestReaderProvisionRequiresSavedSMSCBeforeAgentReadback(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1",
		CardID: "89010000000000000001", HardwareProvisionState: "draft",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}}
	if _, _, err := store.PutExpected(line, 1); err != nil {
		t.Fatal(err)
	}
	stub := &readerReadbackStub{}
	handler, _ := NewReaderProvisionHandler(stub, store)
	payload := `{"schema_version":1,"operation_id":"reader-provision-no-smsc","line_id":"line-1","expected_catalog_revision":2,"process_generation":"process-1","reader_name":"reader-1","card_id":"89010000000000000001","sim_session_generation":"session-1"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload)))
	if response.Code != http.StatusUnprocessableEntity || stub.calls != 0 ||
		!strings.Contains(response.Body.String(), "reader_provision_smsc_required") {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
	}
}

func TestUnknownReaderProvisionDoesNotBlockFreshReadOnlyAttempt(t *testing.T) {
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1",
		CardID: "89010000000000000001", HardwareProvisionState: "draft",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", SMSC: "+447785016005"}}
	if _, _, err := store.PutExpected(line, 1); err != nil {
		t.Fatal(err)
	}
	stub := &readerReadbackStub{err: context.DeadlineExceeded}
	handler, _ := NewReaderProvisionHandler(stub, store)
	firstPayload := `{"schema_version":1,"operation_id":"reader-provision-unknown","line_id":"line-1","expected_catalog_revision":2,"process_generation":"process-1","reader_name":"reader-1","card_id":"89010000000000000001","sim_session_generation":"session-1"}`
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(firstPayload)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("unknown status=%d body=%s", first.Code, first.Body.String())
	}
	stub.err = nil
	stub.result = agentlink.ReaderReadbackResponse{State: "applied", Reader: &agentlink.ReaderFact{
		ReaderName: "reader-1", CardID: line.CardID, CardPresent: true,
		SessionGeneration: "session-1", IdentityState: agentlink.CardIdentified,
		SIM: &agentlink.ReaderSIMFact{IdentityState: "ready", IMSI: "234100000000001", MCC: "234", MNC: "10"}}}
	secondPayload := strings.Replace(firstPayload, "reader-provision-unknown", "reader-provision-fresh", 1)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(secondPayload)))
	if second.Code != http.StatusOK || stub.calls != 2 {
		t.Fatalf("fresh status=%d calls=%d body=%s", second.Code, stub.calls, second.Body.String())
	}
	stored, _, err := store.GetWithRevision("line-1")
	if err != nil || stored.HardwareProvisionState != "provisioned" || stored.Enabled {
		t.Fatalf("stored=%+v error=%v", stored, err)
	}
}

func TestReaderProvisionRejectsMissingOrMismatchedFreshSIMIdentity(t *testing.T) {
	for name, sim := range map[string]*agentlink.ReaderSIMFact{
		"missing":      nil,
		"PIN required": {IdentityState: "pin_required", ErrorCode: "reader_sim_pin_required"},
		"partial":      {IdentityState: "partial", IMSI: "234100000000001", MCC: "234", ErrorCode: "reader_sim_mnc_length_unavailable"},
		"mismatch":     {IdentityState: "ready", IMSI: "234100000000002", MCC: "234", MNC: "10"},
	} {
		t.Run(name, func(t *testing.T) {
			store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			line := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1",
				CardID: "89010000000000000001", HardwareProvisionState: "draft",
				SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", SMSC: "+447785016005"}}
			if _, _, err := store.PutExpected(line, 1); err != nil {
				t.Fatal(err)
			}
			stub := &readerReadbackStub{result: agentlink.ReaderReadbackResponse{State: "applied",
				Reader: &agentlink.ReaderFact{ReaderName: "reader-1", CardID: line.CardID, CardPresent: true,
					SessionGeneration: "session-1", IdentityState: agentlink.CardIdentified, SIM: sim}}}
			handler, _ := NewReaderProvisionHandler(stub, store)
			payload := `{"schema_version":1,"operation_id":"reader-provision-identity","line_id":"line-1","expected_catalog_revision":2,"process_generation":"process-1","reader_name":"reader-1","card_id":"89010000000000000001","sim_session_generation":"session-1"}`
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload)))
			if response.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			stored, revision, err := store.GetWithRevision(line.ID)
			if err != nil || revision != 2 || stored.HardwareProvisionState != "draft" || stored.Enabled {
				t.Fatalf("stored=%+v revision=%d error=%v", stored, revision, err)
			}
			receipt, found, err := store.GetOperation("reader-provision-identity")
			if err != nil || !found || receipt.State != linecatalog.OperationFailed {
				t.Fatalf("receipt=%+v found=%v error=%v", receipt, found, err)
			}
		})
	}
}

func TestReaderReadbackOutcomePreservesOriginalCauseAndDispatchCertainty(t *testing.T) {
	for _, kind := range []string{"readback", "provision"} {
		for _, outcome := range []string{"unknown", "failed", "unsupported"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				line := linecatalog.Line{SchemaVersion: linecatalog.SchemaVersion, ID: "line-1", CardID: "89010000000000000001",
					HardwareProvisionState: "draft", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", SMSC: "+447785016005"}}
				if _, _, err := store.PutExpected(line, 1); err != nil {
					t.Fatal(err)
				}
				code, state := "reader_readback_secure_element_failed", outcome
				stub := &readerReadbackStub{result: agentlink.ReaderReadbackResponse{State: outcome, ErrorCode: code}}
				if outcome == "unsupported" {
					stub.err = agentlink.ErrReaderSIMIdentityUnsupported
					code, state = "reader_sim_identity_unsupported", "failed"
				}
				var handler http.Handler
				payload := map[string]any{"operation_id": "scoped-outcome", "process_generation": "process-1",
					"reader_name": "reader-1", "card_id": line.CardID, "sim_session_generation": "session-1"}
				if outcome == "unsupported" {
					payload["scope"] = "sim_identity"
				}
				if kind == "provision" {
					payload["schema_version"], payload["line_id"], payload["expected_catalog_revision"] = 1, line.ID, 2
					handler, err = NewReaderProvisionHandler(stub, store)
				} else {
					handler, err = NewReaderReadbackHandler(stub, store)
				}
				if err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(payload)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(data))))
				var body map[string]any
				if json.Unmarshal(response.Body.Bytes(), &body) != nil {
					t.Fatal(response.Body.String())
				}
				if body["error_code"] != code && body["code"] != code {
					t.Fatalf("READBACK_CAUSE: lost original cause: %s", response.Body.String())
				}
				if outcome == "unsupported" && (response.Code != http.StatusConflict || body["not_dispatched"] != true) {
					t.Fatalf("dispatch certainty: %s", response.Body.String())
				}
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.SetPathValue("operationID", "scoped-outcome")
				status := httptest.NewRecorder()
				linecatalog.NewOperationHandler(store).ServeHTTP(status, req)
				var saved map[string]any
				_ = json.Unmarshal(status.Body.Bytes(), &saved)
				if status.Code != 200 || saved["state"] != state || saved["error_code"] != code {
					t.Fatalf("READBACK_CAUSE: durable result=%s", status.Body.String())
				}
				stored, revision, err := store.GetWithRevision(line.ID)
				if err != nil || revision != 2 || stored.Enabled || stored.HardwareProvisionState != "draft" {
					t.Fatal("failed observation promoted catalog")
				}
				if outcome == "unsupported" {
					delete(payload, "scope")
				} else {
					payload["scope"] = "sim_identity"
				}
				changed, _ := json.Marshal(payload)
				retry := httptest.NewRecorder()
				handler.ServeHTTP(retry, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(changed))))
				if retry.Code != http.StatusConflict || stub.calls != 1 {
					t.Fatal("existing operation reinterpreted under a different scope")
				}
			})
		}
	}
}
