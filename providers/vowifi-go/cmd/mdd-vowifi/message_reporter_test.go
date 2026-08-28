// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/service"
)

const reporterTestToken = "0123456789abcdef0123456789abcdef"

func TestMessageReporterAdoptsDurableOutboxAndDeletesOnlyAfterCoreAccepts(t *testing.T) {
	accepted := make(chan providermessages.Event, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/provider/messages" || request.Header.Get("Authorization") != "Bearer "+reporterTestToken {
			http.Error(response, "unexpected request", http.StatusBadRequest)
			return
		}
		var event providermessages.Event
		if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		accepted <- event
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	outbox := service.NewMemoryOperationStore()
	event := providermessages.Event{
		SchemaVersion: providermessages.SchemaVersion, EventID: "old:received:call:1",
		LineID: "line-1", ProviderID: "provider-1", ProcessGeneration: "old",
		Kind: providermessages.KindReceived, ObservedAt: time.Now(), MessageID: "ims:call:1", Sender: "+100",
	}
	if err := outbox.EnqueueMessage(event); err != nil {
		t.Fatal(err)
	}
	settings := config{LineID: "line-1", ProviderID: "provider-1"}
	settings.Core.RegistrationURL = server.URL + "/v1/media/providers"
	settings.Core.RegistrationToken = reporterTestToken
	reporter, err := newMessageReporter(settings, "new", outbox)
	if err != nil {
		t.Fatal(err)
	}
	reporter.flush(context.Background())
	select {
	case reported := <-accepted:
		if reported.ProcessGeneration != "new" || reported.EventID != event.EventID {
			t.Fatalf("reported=%+v", reported)
		}
	default:
		t.Fatal("Core did not receive adopted message")
	}
	if pending, err := outbox.PendingMessages(10); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}
