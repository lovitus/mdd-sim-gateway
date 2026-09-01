package allowance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

type Catalog interface {
	Get(string) (linecatalog.Line, error)
}

type MessageStore interface {
	Window(string, time.Time, time.Time, int) ([]providermessages.Record, error)
}

type RouteVerifier interface {
	VerifyMessageRoute(context.Context, string, string, string) error
}

type RouteVerifierFunc func(context.Context, string, string, string) error

func (function RouteVerifierFunc) VerifyMessageRoute(ctx context.Context,
	transport, lineID, cardID string) error {
	return function(ctx, transport, lineID, cardID)
}

type Service struct {
	store    *Store
	catalog  Catalog
	messages MessageStore
	routes   RouteVerifier
	now      func() time.Time
}

func New(store *Store, catalog Catalog, messages MessageStore, routes RouteVerifier) (*Service, error) {
	if store == nil || catalog == nil || messages == nil || routes == nil {
		return nil, errors.New("invalid allowance service configuration")
	}
	return &Service{store: store, catalog: catalog, messages: messages, routes: routes, now: time.Now}, nil
}

func (service *Service) Snapshot(_ context.Context, lineID string) (Snapshot, error) {
	if _, err := service.line(lineID); err != nil {
		return Snapshot{}, err
	}
	return service.store.Snapshot(lineID)
}

func (service *Service) Rule(lineID string) (QueryRule, error) {
	if _, err := service.line(lineID); err != nil {
		return QueryRule{}, err
	}
	return service.store.Rule(lineID)
}

func (service *Service) QueryView(ctx context.Context, lineID string) (QueryView, error) {
	if _, err := service.line(lineID); err != nil {
		return QueryView{}, err
	}
	return service.reconcile(ctx, lineID)
}

func (service *Service) CreateQuery(ctx context.Context, lineID string,
	request QueryRequest) (QueryView, bool, error) {
	line, err := service.line(lineID)
	if err != nil {
		return QueryView{}, false, err
	}
	request.QueryID = strings.TrimSpace(request.QueryID)
	request.ExpectedCardID = strings.TrimSpace(request.ExpectedCardID)
	request.Transport = strings.ToLower(strings.TrimSpace(request.Transport))
	digest, err := queryRequestHash(line.ID, request)
	if err != nil {
		return QueryView{}, false, err
	}
	if existing, found, lookupErr := service.store.QueryByID(request.QueryID); lookupErr != nil {
		return QueryView{}, false, lookupErr
	} else if found {
		if existing.RequestSHA256 != digest {
			return QueryView{}, false, ErrQueryConflict
		}
		view, viewErr := service.viewForQuery(existing, service.now().UTC())
		return view, false, viewErr
	}
	if line.CardID != request.ExpectedCardID || !line.Enabled {
		return QueryView{}, false, ErrRouteUnavailable
	}
	if err := service.routes.VerifyMessageRoute(ctx, request.Transport, line.ID, request.ExpectedCardID); err != nil {
		return QueryView{}, false, errors.Join(ErrRouteUnavailable, err)
	}
	rule, err := service.store.Rule(line.ID)
	if err != nil {
		return QueryView{}, false, err
	}
	if rule.Recipient == "" || rule.Body == "" {
		return QueryView{}, false, ErrRuleUnavailable
	}
	operationID, err := randomID("allowance-sms-")
	if err != nil {
		return QueryView{}, false, err
	}
	messageID, err := randomID("allowance-message-")
	if err != nil {
		return QueryView{}, false, err
	}
	candidate := Query{
		QueryID: request.QueryID, RequestSHA256: digest, LineID: line.ID,
		ExpectedCardID: request.ExpectedCardID, Transport: request.Transport,
		RuleRevision: rule.Revision, Recipient: rule.Recipient, Body: rule.Body, Parser: rule.Parser,
		OperationID: operationID, MessageID: messageID,
	}
	query, created, err := service.store.BeginQuery(candidate, service.now().UTC())
	if err != nil {
		return QueryView{}, false, err
	}
	view, err := service.viewForQuery(query, service.now().UTC())
	return view, created, err
}

func (service *Service) CloseQuery(lineID, queryID string) (QueryView, error) {
	if _, err := service.line(lineID); err != nil {
		return QueryView{}, err
	}
	query, err := service.store.CloseQuery(lineID, queryID)
	if err != nil {
		return QueryView{}, err
	}
	return service.viewForQuery(query, service.now().UTC())
}

// AuthorizeDispatch validates an already-created immutable allowance query at
// the final paid SMS boundary. Closing or staling a query immediately revokes
// every old browser copy of its dispatch payload.
func (service *Service) AuthorizeDispatch(queryID, transport, lineID, expectedCardID,
	operationID, messageID, recipient, body string) error {
	_, err := service.store.AuthorizeDispatch(queryID, transport, lineID, expectedCardID,
		operationID, messageID, recipient, body, service.now().UTC())
	return err
}

func (service *Service) reconcile(ctx context.Context, lineID string) (QueryView, error) {
	query, found, err := service.store.Query(lineID)
	if err != nil || !found {
		return QueryView{Replies: []Reply{}}, err
	}
	now := service.now().UTC()
	if query.State == QueryClosed || query.State == QueryStale {
		return service.viewForQuery(query, now)
	}
	if query.State == QueryReplied {
		return service.viewForQueryWithReplies(query, now)
	}
	if err := service.routes.VerifyMessageRoute(ctx, query.Transport, query.LineID, query.ExpectedCardID); err != nil {
		updated, _, _, applyErr := service.store.ApplyReconcile(ReconcileUpdate{Expected: query, Stale: true})
		if errors.Is(applyErr, ErrQueryChanged) {
			return service.currentView(query.LineID, now)
		}
		if applyErr != nil {
			return QueryView{}, applyErr
		}
		return service.viewForQuery(updated, now)
	}
	windowEnd := now
	if query.State == QuerySent && windowEnd.After(query.CorrelationUntil) {
		windowEnd = query.CorrelationUntil
	}
	records, err := service.messages.Window(query.LineID, query.CreatedAt, windowEnd, MaximumWindowRecords)
	if err != nil {
		if errors.Is(err, providermessages.ErrWindowTooLarge) {
			return scanFailureView(query, now, "message_window_too_large"), nil
		}
		return QueryView{}, err
	}
	if query.State == QueryPrepared {
		sentAt, found, conflict := submittedAt(records, query)
		if conflict {
			return QueryView{}, errors.New("allowance submitted message identity conflicts")
		}
		if !found {
			return service.viewForQuery(query, now)
		}
		correlationUntil := sentAt.Add(QueryWindow)
		updated, _, _, applyErr := service.store.ApplyReconcile(ReconcileUpdate{
			Expected: query, SentAt: sentAt, CorrelationUntil: correlationUntil,
		})
		if errors.Is(applyErr, ErrQueryChanged) {
			return service.currentView(query.LineID, now)
		}
		if applyErr != nil {
			return QueryView{}, applyErr
		}
		query = updated
		windowEnd = now
		if windowEnd.After(query.CorrelationUntil) {
			windowEnd = query.CorrelationUntil
		}
		records, err = service.messages.Window(query.LineID, query.CreatedAt, windowEnd, MaximumWindowRecords)
		if err != nil {
			if errors.Is(err, providermessages.ErrWindowTooLarge) {
				return scanFailureView(query, now, "message_window_too_large"), nil
			}
			return QueryView{}, err
		}
	}
	replies, err := correlatedReplies(records, query)
	if err != nil {
		if errors.Is(err, ErrReplyWindowTooLarge) {
			return scanFailureView(query, now, "reply_window_too_large"), nil
		}
		return QueryView{}, err
	}
	parsed := parseReplies(query.Parser, replies)
	complete := !now.Before(query.CorrelationUntil) && len(replies) != 0
	replyCode := ""
	if len(replies) != 0 {
		replyCode = "raw_reply"
		if query.Parser != ParserNone {
			replyCode = "no_match"
			if len(parsed) != 0 {
				replyCode = "parsed"
			}
		}
	}
	updatedAt := time.Time{}
	if len(replies) != 0 {
		updatedAt = replies[len(replies)-1].ReceivedAt
	}
	updated, _, _, applyErr := service.store.ApplyReconcile(ReconcileUpdate{
		Expected: query, Parsed: parsed, UpdatedAt: updatedAt,
		ReplyCount: len(replies), ReplyCode: replyCode, Complete: complete,
	})
	if errors.Is(applyErr, ErrQueryChanged) {
		return service.currentView(query.LineID, now)
	}
	if applyErr != nil {
		return QueryView{}, applyErr
	}
	return service.viewForQueryWithReplies(updated, now)
}

func (service *Service) currentView(lineID string, now time.Time) (QueryView, error) {
	current, found, err := service.store.Query(lineID)
	if err != nil || !found {
		return QueryView{Replies: []Reply{}}, err
	}
	if current.State == QueryReplied {
		return service.viewForQueryWithReplies(current, now)
	}
	return service.viewForQuery(current, now)
}

func (service *Service) viewForQuery(query Query, now time.Time) (QueryView, error) {
	copy := query
	return QueryView{
		Query: &copy, Dispatch: dispatchFor(query), Replies: []Reply{},
		Expired: (query.State == QueryPrepared || query.State == QuerySent) && now.After(query.CorrelationUntil),
		Code:    queryViewCode(query, now),
	}, nil
}

func (service *Service) viewForQueryWithReplies(query Query, now time.Time) (QueryView, error) {
	view, _ := service.viewForQuery(query, now)
	if query.SentAt.IsZero() {
		return view, nil
	}
	records, err := service.messages.Window(query.LineID, query.SentAt, query.CorrelationUntil, MaximumWindowRecords)
	if err != nil {
		if errors.Is(err, providermessages.ErrWindowTooLarge) {
			return scanFailureView(query, now, "message_window_too_large"), nil
		}
		return QueryView{}, err
	}
	view.Replies, err = correlatedReplies(records, query)
	if errors.Is(err, ErrReplyWindowTooLarge) {
		return scanFailureView(query, now, "reply_window_too_large"), nil
	}
	return view, err
}

func scanFailureView(query Query, now time.Time, code string) QueryView {
	copy := query
	return QueryView{Query: &copy, Replies: []Reply{}, Expired: now.After(query.CorrelationUntil), Code: code}
}

func dispatchFor(query Query) *Dispatch {
	if query.OperationID == "" || query.MessageID == "" ||
		(query.State != QueryPrepared && query.State != QuerySent) {
		return nil
	}
	path := fmt.Sprintf("/v1/lines/%s/%s/messages", url.PathEscape(query.LineID), query.Transport)
	if query.Transport == "vowifi" {
		path += "/send"
	}
	return &Dispatch{Method: http.MethodPost, Path: path, Body: map[string]any{
		"operation_id": query.OperationID, "message_id": query.MessageID,
		"recipient": query.Recipient, "body": query.Body, "expected_card_id": query.ExpectedCardID,
		"allowance_query_id": query.QueryID,
	}}
}

func submittedAt(records []providermessages.Record, query Query) (time.Time, bool, bool) {
	var result time.Time
	found, conflict := false, false
	providerID := ""
	type partIdentity struct {
		callID string
		rpmr   int
		state  string
	}
	parts := make(map[int]partIdentity)
	for _, record := range records {
		if record.Kind != providermessages.KindSubmitted || record.MessageID != query.MessageID {
			continue
		}
		if record.LineID != query.LineID || strings.TrimSpace(record.Recipient) != query.Recipient ||
			record.Part < 1 || (providerID != "" && record.ProviderID != providerID) {
			conflict = true
			continue
		}
		providerID = record.ProviderID
		identity := partIdentity{callID: strings.TrimSpace(record.CallID), rpmr: record.RPMR, state: strings.TrimSpace(record.State)}
		if prior, exists := parts[record.Part]; exists && prior != identity {
			conflict = true
			continue
		}
		parts[record.Part] = identity
		if !found || record.ReceivedAt.Before(result) {
			result = record.ReceivedAt.UTC()
		}
		found = true
	}
	if conflict || !found {
		return result, found, conflict
	}
	for part := 1; part <= len(parts); part++ {
		if _, exists := parts[part]; !exists {
			return time.Time{}, false, false
		}
	}
	return result, found, conflict
}

func correlatedReplies(records []providermessages.Record, query Query) ([]Reply, error) {
	seen := make(map[string]struct{})
	replies := make([]Reply, 0)
	totalBytes := 0
	for _, record := range records {
		if record.Kind != providermessages.KindReceived || record.LineID != query.LineID ||
			strings.TrimSpace(record.Sender) != query.Recipient || record.ReceivedAt.Before(query.SentAt) ||
			record.ReceivedAt.After(query.CorrelationUntil) {
			continue
		}
		identity := record.ProviderID + "\x00" + record.EventID
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		totalBytes += len(record.Body)
		if totalBytes > MaximumReplyBytes {
			return nil, ErrReplyWindowTooLarge
		}
		replies = append(replies, Reply{
			EventID: record.EventID, Sender: record.Sender, Body: record.Body,
			ObservedAt: record.ObservedAt, ReceivedAt: record.ReceivedAt,
		})
	}
	sort.SliceStable(replies, func(left, right int) bool {
		if replies[left].ReceivedAt.Equal(replies[right].ReceivedAt) {
			return replies[left].EventID < replies[right].EventID
		}
		return replies[left].ReceivedAt.Before(replies[right].ReceivedAt)
	})
	return replies, nil
}

func queryViewCode(query Query, now time.Time) string {
	if (query.State == QueryPrepared || query.State == QuerySent) && now.After(query.CorrelationUntil) {
		if query.State == QueryPrepared {
			return "dispatch_unconfirmed"
		}
		return "no_reply"
	}
	return query.ReplyCode
}

func (service *Service) line(lineID string) (linecatalog.Line, error) {
	lineID = strings.TrimSpace(lineID)
	if !validID(lineID) {
		return linecatalog.Line{}, linecatalog.ErrNotFound
	}
	return service.catalog.Get(lineID)
}

func randomID(prefix string) (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(token[:]), nil
}
