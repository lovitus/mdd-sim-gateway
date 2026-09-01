package allowance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

const (
	testLineID = "line-1"
	testCardID = "8944100000000000001"
)

type testCatalog struct {
	line linecatalog.Line
	err  error
}

func (catalog testCatalog) Get(lineID string) (linecatalog.Line, error) {
	if catalog.err != nil {
		return linecatalog.Line{}, catalog.err
	}
	if lineID != catalog.line.ID {
		return linecatalog.Line{}, linecatalog.ErrNotFound
	}
	return catalog.line, nil
}

type testRoutes struct {
	err   error
	calls []string
}

func (routes *testRoutes) VerifyMessageRoute(_ context.Context, transport, lineID, cardID string) error {
	routes.calls = append(routes.calls, strings.Join([]string{transport, lineID, cardID}, "/"))
	return routes.err
}

func TestSnapshotAndRuleUseIndependentCASAndRuneLimits(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC)
	snapshot, err := store.Snapshot(testLineID)
	if err != nil || snapshot.Revision != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	values := Values{Balance: "£5", ActivatedAt: "2026-09-01"}
	snapshot, changed, err := store.PutSnapshotExpected(testLineID, 1, values, now)
	if err != nil || !changed || snapshot.Revision != 2 || snapshot.Source != SourceManual {
		t.Fatalf("snapshot=%+v changed=%t err=%v", snapshot, changed, err)
	}
	snapshot, changed, err = store.PutSnapshotExpected(testLineID, 2, values, now.Add(time.Second))
	if err != nil || changed || snapshot.Revision != 2 || !snapshot.UpdatedAt.Equal(now) {
		t.Fatalf("no-op snapshot=%+v changed=%t err=%v", snapshot, changed, err)
	}
	if _, _, err := store.PutSnapshotExpected(testLineID, 1, values, now); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale snapshot err=%v", err)
	}
	tooLong := strings.Repeat("界", MaximumValueRunes+1)
	if _, _, err := store.PutSnapshotExpected(testLineID, 2, Values{Balance: tooLong}, now); err == nil {
		t.Fatal("overlong rune value was accepted")
	}
	if _, _, err := store.PutSnapshotExpected(testLineID, 2, Values{ActivatedAt: "09/01/2026"}, now); err == nil {
		t.Fatal("non-YYYY-MM-DD activation date was accepted")
	}

	rule, err := store.Rule(testLineID)
	if err != nil || rule.Revision != 1 || rule.Parser != ParserNone {
		t.Fatalf("rule=%+v err=%v", rule, err)
	}
	rule, changed, err = store.PutRuleExpected(testLineID, 1, QueryRule{
		Recipient: "6700", Body: "BAL", Parser: ParserUltraV1,
	}, now)
	if err != nil || !changed || rule.Revision != 2 {
		t.Fatalf("rule=%+v changed=%t err=%v", rule, changed, err)
	}
	rule, changed, err = store.PutRuleExpected(testLineID, 2, rule, now.Add(time.Second))
	if err != nil || changed || rule.Revision != 2 {
		t.Fatalf("no-op rule=%+v changed=%t err=%v", rule, changed, err)
	}
	rule, changed, err = store.DeleteRuleExpected(testLineID, 2, now.Add(2*time.Second))
	if err != nil || !changed || rule.Revision != 3 || rule.Recipient != "" || rule.Parser != ParserNone {
		t.Fatalf("reset rule=%+v changed=%t err=%v", rule, changed, err)
	}
}

func TestQueryCreationReplayCloseAndCorrelationQuarantine(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	first := queryCandidate(t, "query-1", testLineID, testCardID, "vowifi")
	query, created, err := store.BeginQuery(first, now)
	if err != nil || !created || query.State != QueryPrepared || !query.CorrelationUntil.Equal(now.Add(QueryWindow)) {
		t.Fatalf("query=%+v created=%t err=%v", query, created, err)
	}
	replay := first
	replay.OperationID, replay.MessageID = "new-operation-must-not-win", "new-message-must-not-win"
	replayed, created, err := store.BeginQuery(replay, now.Add(time.Second))
	if err != nil || created || replayed.OperationID != first.OperationID || replayed.MessageID != first.MessageID {
		t.Fatalf("replayed=%+v created=%t err=%v", replayed, created, err)
	}
	conflict := first
	conflict.RequestSHA256 = strings.Repeat("f", 64)
	if _, _, err := store.BeginQuery(conflict, now); !errors.Is(err, ErrQueryConflict) {
		t.Fatalf("same-id conflict err=%v", err)
	}
	closed, err := store.CloseQuery(testLineID, query.QueryID)
	if err != nil || closed.State != QueryClosed {
		t.Fatalf("closed=%+v err=%v", closed, err)
	}
	second := queryCandidate(t, "query-2", testLineID, testCardID, "vowifi")
	if _, _, err := store.BeginQuery(second, now.Add(30*time.Second)); !errors.Is(err, ErrQueryActive) {
		t.Fatalf("query bypassed correlation quarantine: %v", err)
	}
	if _, created, err := store.BeginQuery(second, now.Add(QueryWindow+time.Second)); err != nil || !created {
		t.Fatalf("query after quarantine created=%t err=%v", created, err)
	}
}

func TestCloseAndManualWriteCannotBeOverwrittenByStaleReconcile(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	query, _, err := store.BeginQuery(queryCandidate(t, "query-cas", testLineID, testCardID, "vowifi"), now)
	if err != nil {
		t.Fatal(err)
	}
	sentAt := now.Add(time.Second)
	query, _, _, err = store.ApplyReconcile(ReconcileUpdate{
		Expected: query, SentAt: sentAt, CorrelationUntil: sentAt.Add(QueryWindow),
	})
	if err != nil || query.State != QuerySent {
		t.Fatalf("sent query=%+v err=%v", query, err)
	}
	staleScan := query
	if _, err := store.CloseQuery(testLineID, query.QueryID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ApplyReconcile(ReconcileUpdate{
		Expected: staleScan, Parsed: map[string]string{"balance": "$9"}, UpdatedAt: now.Add(2 * time.Second),
	}); !errors.Is(err, ErrQueryChanged) {
		t.Fatalf("closed query was revived: %v", err)
	}

	query2, _, err := store.BeginQuery(queryCandidate(t, "query-merge", testLineID, testCardID, "vowifi"),
		now.Add(2*QueryWindow))
	if err != nil {
		t.Fatal(err)
	}
	query2, _, _, err = store.ApplyReconcile(ReconcileUpdate{
		Expected: query2, SentAt: now.Add(2*QueryWindow + time.Second),
		CorrelationUntil: now.Add(3*QueryWindow + time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	manual, _, err := store.PutSnapshotExpected(testLineID, 1, Values{DataRemaining: "1 GB"}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, merged, changed, err := store.ApplyReconcile(ReconcileUpdate{
		Expected: query2, Parsed: map[string]string{"balance": "$5"}, UpdatedAt: now.Add(time.Second),
	})
	if err != nil || !changed || merged.Revision != manual.Revision+1 || merged.Values.Balance != "$5" ||
		merged.Values.DataRemaining != "1 GB" {
		t.Fatalf("merged=%+v changed=%t err=%v", merged, changed, err)
	}
	_, repeated, changed, err := store.ApplyReconcile(ReconcileUpdate{
		Expected: query2, Parsed: map[string]string{"balance": "$5"}, UpdatedAt: now.Add(2 * time.Second),
	})
	if err != nil || changed || repeated.Revision != merged.Revision {
		t.Fatalf("repeat merged=%+v changed=%t err=%v", repeated, changed, err)
	}
}

func TestServiceCorrelatesOnlyAfterSubmittedUsingCoreReceiveTimeAndMergesMultipart(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	service, store, messages, routes := testService(t, now)
	putRule(t, store, now, ParserUltraV1)
	view, created, err := service.CreateQuery(context.Background(), testLineID, QueryRequest{
		QueryID: "query-service", ExpectedCardID: testCardID, Transport: "vowifi",
	})
	if err != nil || !created || view.Dispatch == nil || view.Dispatch.Body["expected_card_id"] != testCardID ||
		view.Dispatch.Body["allowance_query_id"] != "query-service" || len(routes.calls) != 1 {
		t.Fatalf("view=%+v created=%t routes=%v err=%v", view, created, routes.calls, err)
	}
	query := *view.Query
	if err := service.AuthorizeDispatch(query.QueryID, query.Transport, query.LineID, query.ExpectedCardID,
		query.OperationID, query.MessageID, query.Recipient, query.Body); err != nil {
		t.Fatalf("authorize dispatch: %v", err)
	}
	if err := service.AuthorizeDispatch(query.QueryID, "cellular", query.LineID, query.ExpectedCardID,
		query.OperationID, query.MessageID, query.Recipient, query.Body); !errors.Is(err, ErrQueryChanged) {
		t.Fatalf("wrong transport dispatch authorized: %v", err)
	}
	acceptMessage(t, messages, providermessages.Event{
		SchemaVersion: 1, EventID: "early-received", LineID: testLineID, ProviderID: "provider-1",
		ProcessGeneration: "process-1", Kind: providermessages.KindReceived,
		ObservedAt: now.Add(24 * time.Hour), Sender: "6700", Body: "钱包余额：$999",
	}, now.Add(time.Second))
	service.now = func() time.Time { return now.Add(2 * time.Second) }
	view, err = service.QueryView(context.Background(), testLineID)
	if err != nil || view.Query.State != QueryPrepared || len(view.Replies) != 0 {
		t.Fatalf("pre-submit view=%+v err=%v", view, err)
	}
	acceptMessage(t, messages, submittedEvent(query, "6700"), now.Add(3*time.Second))
	acceptMessage(t, messages, providermessages.Event{
		SchemaVersion: 1, EventID: "wrong-sender", LineID: testLineID, ProviderID: "provider-1",
		ProcessGeneration: "process-1", Kind: providermessages.KindReceived,
		ObservedAt: now.Add(-24 * time.Hour), Sender: "999", Body: "钱包余额：$500",
	}, now.Add(4*time.Second))
	acceptMessage(t, messages, providermessages.Event{
		SchemaVersion: 1, EventID: "usage-reply", LineID: testLineID, ProviderID: "provider-1",
		ProcessGeneration: "process-1", Kind: providermessages.KindReceived,
		ObservedAt: now.Add(-24 * time.Hour), Sender: "6700",
		Body: "本月剩余通话时间：100 分钟\n本月剩余短信数：89 条\n本月剩余流量：100.0MB\n计划到期日：09/28/2026",
	}, now.Add(5*time.Second))
	service.now = func() time.Time { return now.Add(6 * time.Second) }
	view, err = service.QueryView(context.Background(), testLineID)
	snapshot, _ := store.Snapshot(testLineID)
	if err != nil || view.Query.State != QuerySent || len(view.Replies) != 1 ||
		snapshot.Values.VoiceRemaining != "100 分钟" || snapshot.Values.SMSRemaining != "89 条" ||
		snapshot.Values.Balance != "" {
		t.Fatalf("partial view=%+v snapshot=%+v err=%v", view, snapshot, err)
	}
	acceptMessage(t, messages, providermessages.Event{
		SchemaVersion: 1, EventID: "balance-reply", LineID: testLineID, ProviderID: "provider-1",
		ProcessGeneration: "process-1", Kind: providermessages.KindReceived,
		ObservedAt: now.Add(365 * 24 * time.Hour), Sender: "6700", Body: "钱包余额：$5",
	}, now.Add(8*time.Second))
	service.now = func() time.Time { return now.Add(9 * time.Second) }
	view, err = service.QueryView(context.Background(), testLineID)
	snapshot, _ = store.Snapshot(testLineID)
	if err != nil || view.Query.State != QuerySent || len(view.Replies) != 2 || snapshot.Values.Balance != "$5" {
		t.Fatalf("merged view=%+v snapshot=%+v err=%v", view, snapshot, err)
	}
	completeAt := view.Query.CorrelationUntil.Add(time.Second)
	service.now = func() time.Time { return completeAt }
	view, err = service.QueryView(context.Background(), testLineID)
	if err != nil || view.Query.State != QueryReplied || view.Query.ReplyCode != "parsed" {
		t.Fatalf("complete view=%+v err=%v", view, err)
	}
}

func TestServiceClosePreventsLateReplyAndBlocksNewQueryUntilQuarantineEnds(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	service, store, messages, _ := testService(t, now)
	putRule(t, store, now, ParserCTExcelV1)
	view, _, err := service.CreateQuery(context.Background(), testLineID, QueryRequest{
		QueryID: "query-close", ExpectedCardID: testCardID, Transport: "vowifi",
	})
	if err != nil {
		t.Fatal(err)
	}
	query := *view.Query
	service.now = func() time.Time { return now.Add(time.Second) }
	if err := service.AuthorizeDispatch(query.QueryID, query.Transport, query.LineID, query.ExpectedCardID,
		query.OperationID, query.MessageID, query.Recipient, query.Body); err != nil {
		t.Fatal(err)
	}
	authorized, found, err := store.QueryByID(query.QueryID)
	if err != nil || !found || !authorized.DispatchAuthorizedAt.Equal(now.Add(time.Second)) ||
		!authorized.CorrelationUntil.Equal(now.Add(time.Second+MaximumDispatchUncertainty+QueryWindow)) {
		t.Fatalf("authorized=%+v found=%t err=%v", authorized, found, err)
	}
	service.now = func() time.Time { return now.Add(10 * time.Second) }
	if _, err := service.CloseQuery(testLineID, query.QueryID); err != nil {
		t.Fatal(err)
	}
	if err := service.AuthorizeDispatch(query.QueryID, query.Transport, query.LineID, query.ExpectedCardID,
		query.OperationID, query.MessageID, query.Recipient, query.Body); !errors.Is(err, ErrQueryChanged) {
		t.Fatalf("closed dispatch remained authorized: %v", err)
	}
	acceptMessage(t, messages, submittedEvent(query, "6700"), now.Add(134*time.Second))
	acceptMessage(t, messages, providermessages.Event{
		SchemaVersion: 1, EventID: "late-reply", LineID: testLineID, ProviderID: "provider-1",
		ProcessGeneration: "process-1", Kind: providermessages.KindReceived,
		ObservedAt: now, Sender: "6700", Body: "Your current credit balance is £99.",
	}, now.Add(140*time.Second))
	if _, err := service.Snapshot(context.Background(), testLineID); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot(testLineID)
	if snapshot.Values.Balance != "" {
		t.Fatalf("late closed reply changed snapshot: %+v", snapshot)
	}
	service.now = func() time.Time { return now.Add(134 * time.Second) }
	if _, _, err := service.CreateQuery(context.Background(), testLineID, QueryRequest{
		QueryID: "query-too-soon", ExpectedCardID: testCardID, Transport: "vowifi",
	}); !errors.Is(err, ErrQueryActive) {
		t.Fatalf("new query bypassed quarantine: %v", err)
	}
	service.now = func() time.Time { return authorized.CorrelationUntil.Add(time.Second) }
	if _, created, err := service.CreateQuery(context.Background(), testLineID, QueryRequest{
		QueryID: "query-after", ExpectedCardID: testCardID, Transport: "vowifi",
	}); err != nil || !created {
		t.Fatalf("query after quarantine created=%t err=%v", created, err)
	}
}

func TestServiceMarksRouteStaleAndParserNoneLeavesSnapshotUntouched(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	service, store, messages, routes := testService(t, now)
	putRule(t, store, now, ParserNone)
	view, _, err := service.CreateQuery(context.Background(), testLineID, QueryRequest{
		QueryID: "query-raw", ExpectedCardID: testCardID, Transport: "cellular",
	})
	if err != nil {
		t.Fatal(err)
	}
	query := *view.Query
	acceptMessage(t, messages, submittedEvent(query, "6700"), now.Add(time.Second))
	acceptMessage(t, messages, providermessages.Event{
		SchemaVersion: 1, EventID: "raw-reply", LineID: testLineID, ProviderID: "agent-1",
		ProcessGeneration: "process-1", Kind: providermessages.KindReceived,
		ObservedAt: now, Sender: "6700", Body: "unstructured carrier response",
	}, now.Add(2*time.Second))
	service.now = func() time.Time { return now.Add(3 * time.Second) }
	view, err = service.QueryView(context.Background(), testLineID)
	if err != nil || view.Query.State != QuerySent || len(view.Replies) != 1 {
		t.Fatalf("raw view=%+v err=%v", view, err)
	}
	snapshot, _ := store.Snapshot(testLineID)
	if snapshot.Revision != 1 || snapshot.Source != "" {
		t.Fatalf("parser none changed snapshot: %+v", snapshot)
	}
	routes.err = errors.New("duplicate or offline card route")
	view, err = service.QueryView(context.Background(), testLineID)
	if err != nil || view.Query.State != QueryStale || len(view.Replies) != 0 {
		t.Fatalf("stale view=%+v err=%v", view, err)
	}
}

func TestPreparedQueryRestoresAcrossRestartAndNoMatchCompletesAtWindowEnd(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "allowance.db")
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	putRule(t, store, now, ParserCTExcelV1)
	messages, err := providermessages.OpenStore(filepath.Join(root, "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	routes := &testRoutes{}
	service, _ := New(store, testCatalog{line: linecatalog.Line{SchemaVersion: 1, ID: testLineID,
		Enabled: true, CardID: testCardID, SIM: linecatalog.SIMConfig{IMEI: "862547055201716"}}}, messages, routes)
	service.now = func() time.Time { return now }
	view, _, err := service.CreateQuery(context.Background(), testLineID, QueryRequest{
		QueryID: "query-restart", ExpectedCardID: testCardID, Transport: "vowifi",
	})
	if err != nil {
		t.Fatal(err)
	}
	query := *view.Query
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, _ = New(store, testCatalog{line: linecatalog.Line{SchemaVersion: 1, ID: testLineID,
		Enabled: true, CardID: testCardID, SIM: linecatalog.SIMConfig{IMEI: "862547055201716"}}}, messages, routes)
	acceptMessage(t, messages, submittedEvent(query, "6700"), now.Add(time.Second))
	acceptMessage(t, messages, providermessages.Event{
		SchemaVersion: 1, EventID: "unmatched-reply", LineID: testLineID, ProviderID: "provider-1",
		ProcessGeneration: "process-1", Kind: providermessages.KindReceived,
		ObservedAt: now, Sender: "6700", Body: "not a known balance format",
	}, now.Add(2*time.Second))
	service.now = func() time.Time { return now.Add(QueryWindow + 2*time.Second) }
	view, err = service.QueryView(context.Background(), testLineID)
	if err != nil || view.Query.State != QueryReplied || view.Query.ReplyCode != "no_match" || len(view.Replies) != 1 {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	snapshot, _ := store.Snapshot(testLineID)
	if snapshot.Revision != 1 {
		t.Fatalf("no-match changed snapshot=%+v", snapshot)
	}
	_ = messages.Close()
}

func TestMessageWindowLimitFailsClosed(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	service, store, messages, _ := testService(t, now)
	putRule(t, store, now, ParserNone)
	view, _, err := service.CreateQuery(context.Background(), testLineID, QueryRequest{
		QueryID: "query-window", ExpectedCardID: testCardID, Transport: "vowifi",
	})
	if err != nil {
		t.Fatal(err)
	}
	query := *view.Query
	acceptMessage(t, messages, submittedEvent(query, "6700"), now.Add(time.Second))
	service.now = func() time.Time { return now.Add(2 * time.Second) }
	if _, err := service.QueryView(context.Background(), testLineID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaximumWindowRecords+1; index++ {
		acceptMessage(t, messages, providermessages.Event{
			SchemaVersion: 1, EventID: fmt.Sprintf("reply-%03d", index), LineID: testLineID,
			ProviderID: "provider-1", ProcessGeneration: "process-1", Kind: providermessages.KindReceived,
			ObservedAt: now, Sender: "6700", Body: "x",
		}, now.Add(3*time.Second+time.Duration(index)*time.Millisecond))
	}
	service.now = func() time.Time { return now.Add(10 * time.Second) }
	view, err = service.QueryView(context.Background(), testLineID)
	if err != nil || view.Query == nil || view.Query.QueryID != query.QueryID || view.Dispatch != nil ||
		view.Code != "message_window_too_large" {
		t.Fatalf("window view=%+v err=%v", view, err)
	}
	if _, err := service.CloseQuery(testLineID, query.QueryID); err != nil {
		t.Fatalf("oversized window query could not close: %v", err)
	}
}

func TestReplyByteLimitKeepsQueryClosable(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	service, store, messages, _ := testService(t, now)
	putRule(t, store, now, ParserNone)
	view, _, err := service.CreateQuery(context.Background(), testLineID, QueryRequest{
		QueryID: "query-bytes", ExpectedCardID: testCardID, Transport: "vowifi",
	})
	if err != nil {
		t.Fatal(err)
	}
	query := *view.Query
	acceptMessage(t, messages, submittedEvent(query, "6700"), now.Add(time.Second))
	for index := 0; index < 17; index++ {
		acceptMessage(t, messages, providermessages.Event{
			SchemaVersion: 1, EventID: fmt.Sprintf("large-reply-%02d", index), LineID: testLineID,
			ProviderID: "provider-1", ProcessGeneration: "process-1", Kind: providermessages.KindReceived,
			ObservedAt: now, Sender: "6700", Body: strings.Repeat("x", 16<<10),
		}, now.Add(2*time.Second+time.Duration(index)*time.Millisecond))
	}
	service.now = func() time.Time { return now.Add(10 * time.Second) }
	view, err = service.QueryView(context.Background(), testLineID)
	if err != nil || view.Query == nil || view.Dispatch != nil || view.Code != "reply_window_too_large" {
		t.Fatalf("reply byte view=%+v err=%v", view, err)
	}
	if _, err := service.CloseQuery(testLineID, query.QueryID); err != nil {
		t.Fatalf("oversized reply query could not close: %v", err)
	}
}

func TestSubmittedMultipartRequiresOneConsistentBusinessIdentity(t *testing.T) {
	query := Query{LineID: testLineID, MessageID: "message-parts", Recipient: "6700"}
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	record := func(part int, providerID, callID string) providermessages.Record {
		return providermessages.Record{Event: providermessages.Event{
			Kind: providermessages.KindSubmitted, LineID: testLineID, MessageID: query.MessageID,
			Recipient: query.Recipient, Part: part, ProviderID: providerID, CallID: callID, State: "submitted",
		}, ReceivedAt: now.Add(time.Duration(part) * time.Second)}
	}
	first, duplicate := record(1, "provider-1", "call-1"), record(1, "provider-1", "call-1")
	if sentAt, found, conflict := submittedAt([]providermessages.Record{first, duplicate}, query); !found || conflict || !sentAt.Equal(first.ReceivedAt) {
		t.Fatalf("idempotent duplicate sentAt=%v found=%t conflict=%t", sentAt, found, conflict)
	}
	conflictingPart := record(1, "provider-1", "different-call")
	if _, _, conflict := submittedAt([]providermessages.Record{first, conflictingPart}, query); !conflict {
		t.Fatal("conflicting duplicate part was accepted")
	}
	differentProvider := record(2, "provider-2", "call-2")
	if _, _, conflict := submittedAt([]providermessages.Record{first, differentProvider}, query); !conflict {
		t.Fatal("mixed provider identity was accepted")
	}
	missingPart := record(2, "provider-1", "call-2")
	if _, found, conflict := submittedAt([]providermessages.Record{missingPart}, query); found || conflict {
		t.Fatalf("incomplete multipart found=%t conflict=%t", found, conflict)
	}
	second := record(2, "provider-1", "call-2")
	if _, found, conflict := submittedAt([]providermessages.Record{first, second}, query); !found || conflict {
		t.Fatalf("complete multipart found=%t conflict=%t", found, conflict)
	}
}

func TestHTTPContractUsesSeparateETagsAndStrictQueryIntent(t *testing.T) {
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	service, _, _, _ := testService(t, now)
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	mux := allowanceMux(handler)
	get := request(t, mux, http.MethodGet, "/v1/lines/line-1/allowance", "", "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET status=%d etag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}
	put := request(t, mux, http.MethodPut, "/v1/lines/line-1/allowance", `{"balance":"$4"}`, `"1"`)
	if put.Code != http.StatusOK || put.Header().Get("ETag") != `"2"` {
		t.Fatalf("PUT status=%d etag=%q body=%s", put.Code, put.Header().Get("ETag"), put.Body.String())
	}
	stale := request(t, mux, http.MethodPut, "/v1/lines/line-1/allowance", `{"balance":"$5"}`, `"1"`)
	if stale.Code != http.StatusPreconditionFailed || stale.Header().Get("ETag") != `"2"` {
		t.Fatalf("stale status=%d etag=%q body=%s", stale.Code, stale.Header().Get("ETag"), stale.Body.String())
	}
	rule := request(t, mux, http.MethodPut, "/v1/lines/line-1/allowance/query-rule",
		`{"recipient":"6700","body":"BAL","parser":"ultramobile_v1"}`, `"1"`)
	if rule.Code != http.StatusOK || rule.Header().Get("ETag") != `"2"` {
		t.Fatalf("rule status=%d etag=%q body=%s", rule.Code, rule.Header().Get("ETag"), rule.Body.String())
	}
	created := request(t, mux, http.MethodPost, "/v1/lines/line-1/allowance/query",
		`{"query_id":"query-http","expected_card_id":"8944100000000000001","transport":"vowifi"}`, "")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"expected_card_id":"8944100000000000001"`) {
		t.Fatalf("query status=%d body=%s", created.Code, created.Body.String())
	}
	replay := request(t, mux, http.MethodPost, "/v1/lines/line-1/allowance/query",
		`{"query_id":"query-http","expected_card_id":"8944100000000000001","transport":"vowifi"}`, "")
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	unknown := request(t, mux, http.MethodPost, "/v1/lines/line-1/allowance/query",
		`{"query_id":"query-other","expected_card_id":"8944100000000000001","transport":"vowifi","extra":true}`, "")
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown status=%d body=%s", unknown.Code, unknown.Body.String())
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "allowance.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testService(t *testing.T, now time.Time) (*Service, *Store, *providermessages.Store, *testRoutes) {
	t.Helper()
	store := testStore(t)
	messages, err := providermessages.OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = messages.Close() })
	routes := &testRoutes{}
	service, err := New(store, testCatalog{line: linecatalog.Line{
		SchemaVersion: 1, ID: testLineID, Enabled: true, CardID: testCardID,
		SIM: linecatalog.SIMConfig{IMEI: "862547055201716"},
	}}, messages, routes)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return service, store, messages, routes
}

func putRule(t *testing.T, store *Store, now time.Time, parser string) QueryRule {
	t.Helper()
	rule, _, err := store.PutRuleExpected(testLineID, 1, QueryRule{Recipient: "6700", Body: "BAL", Parser: parser}, now)
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func queryCandidate(t *testing.T, queryID, lineID, cardID, transport string) Query {
	t.Helper()
	digest, err := queryRequestHash(lineID, QueryRequest{QueryID: queryID, ExpectedCardID: cardID, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return Query{QueryID: queryID, RequestSHA256: digest, LineID: lineID, ExpectedCardID: cardID,
		Transport: transport, RuleRevision: 2, Recipient: "6700", Body: "BAL", Parser: ParserUltraV1,
		OperationID: "operation-" + queryID, MessageID: "message-" + queryID}
}

func submittedEvent(query Query, recipient string) providermessages.Event {
	return providermessages.Event{SchemaVersion: 1, EventID: "submitted-" + query.QueryID,
		LineID: query.LineID, ProviderID: "provider-1", ProcessGeneration: "process-1",
		Kind: providermessages.KindSubmitted, ObservedAt: query.CreatedAt, MessageID: query.MessageID,
		Part: 1, Recipient: recipient, CallID: "call-" + query.QueryID, State: "submitted"}
}

func acceptMessage(t *testing.T, store *providermessages.Store, event providermessages.Event, receivedAt time.Time) {
	t.Helper()
	if _, _, err := store.Accept(event, receivedAt); err != nil {
		t.Fatal(err)
	}
}

func allowanceMux(handler http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, pattern := range []string{
		"GET /v1/lines/{lineID}/allowance", "PUT /v1/lines/{lineID}/allowance",
		"GET /v1/lines/{lineID}/allowance/query-rule", "PUT /v1/lines/{lineID}/allowance/query-rule",
		"DELETE /v1/lines/{lineID}/allowance/query-rule", "GET /v1/lines/{lineID}/allowance/query",
		"POST /v1/lines/{lineID}/allowance/query", "DELETE /v1/lines/{lineID}/allowance/query/{queryID}",
	} {
		mux.Handle(pattern, handler)
	}
	return mux
}

func request(t *testing.T, handler http.Handler, method, path, body, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeQueryView(t *testing.T, response *httptest.ResponseRecorder) QueryView {
	t.Helper()
	var view QueryView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}
