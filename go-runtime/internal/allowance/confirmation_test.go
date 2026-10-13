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
			if mode == "store-race" {
				candidate := queryCandidate(t, "racing-query", testLineID, testCardID, "vowifi")
				candidate.Parser = rule.Parser
				if _, _, err := store.PutRuleExpected(testLineID, rule.Revision, QueryRule{Recipient: "6701", Body: "NEW", Parser: ParserNone}, now); err != nil {
					t.Fatal(err)
				}
				if _, created, err := store.BeginQuery(candidate, now); !errors.Is(err, ErrQueryChanged) || created {
					t.Fatalf("CONFIRMATION_FENCE: stale candidate committed=%t err=%v", created, err)
				}
				if _, found, err := store.QueryByID(candidate.QueryID); err != nil || found {
					t.Fatalf("stale candidate survived: found=%t err=%v", found, err)
				}
				return
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
