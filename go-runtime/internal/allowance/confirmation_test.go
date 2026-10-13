package allowance

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConfirmationFence(t *testing.T) {
	for _, mode := range []string{"missing", "revision", "recipient", "body", "store-race", "dispatch-race", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC()
			service, store, _, _ := testService(t, now)
			rule := putRule(t, store, now, ParserNone)
			input := QueryRequest{QueryID: "confirmed-query", ExpectedCardID: testCardID, Transport: "vowifi",
				ExpectedRuleRevision: rule.Revision, Recipient: rule.Recipient, Body: rule.Body}
			switch mode {
			case "missing":
				input.ExpectedRuleRevision, input.Recipient, input.Body = 0, "", ""
			case "revision":
				input.ExpectedRuleRevision--
			case "recipient":
				input.Recipient = "6701"
			case "body":
				input.Body = "different billable text"
			}
			view, created, err := service.CreateQuery(context.Background(), testLineID, input)
			if mode == "missing" || mode == "revision" || mode == "recipient" || mode == "body" {
				if !errors.Is(err, ErrQueryChanged) || created || view.Dispatch != nil {
					t.Fatalf("CONFIRMATION_FENCE: unconfirmed query created=%t view=%+v err=%v", created, view, err)
				}
				return
			}
			if err != nil || !created || view.Dispatch == nil {
				t.Fatalf("confirmed query: %v %+v", err, view)
			}
			query := *view.Query
			if mode != "confirmed" {
				if _, _, err := store.PutRuleExpected(testLineID, rule.Revision, QueryRule{Recipient: "6701", Body: "NEW", Parser: ParserNone}, now); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "store-race" {
				// A candidate prepared before a concurrent rule update must not commit after it.
				query.QueryID = "racing-query"
				if _, _, err := store.BeginQuery(query, now); !errors.Is(err, ErrQueryChanged) {
					t.Fatalf("CONFIRMATION_FENCE: stale rule entered store: %v", err)
				}
				return
			}
			err = service.AuthorizeDispatch(query.QueryID, query.Transport, query.LineID, query.ExpectedCardID, query.OperationID, query.MessageID, query.Recipient, query.Body)
			if mode == "confirmed" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrQueryChanged) {
				t.Fatalf("CONFIRMATION_FENCE: changed rule authorized: %v", err)
			}
		})
	}
}
