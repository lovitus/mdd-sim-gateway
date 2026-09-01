package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookNon2xxAndWrittenDisconnectAreUncertainWithoutHiddenRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("Idempotency-Key") == "" || request.Header.Get("X-MDD-Event-ID") == "" {
			t.Error("stable notification identity headers are missing")
		}
		response.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	delivery, event, config := webhookFixture(server.URL)
	outcome := (HTTPSender{}).sendWebhook(context.Background(), delivery, event, config.Webhook)
	if outcome.State != DeliveryUncertain || !outcome.Wrote || outcome.Retryable || requests.Load() != 1 {
		t.Fatalf("outcome=%+v requests=%d", outcome, requests.Load())
	}

	requests.Store(0)
	broken := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		hijacker, ok := response.(http.Hijacker)
		if !ok {
			t.Fatal("test server cannot hijack")
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.Close()
	}))
	defer broken.Close()
	delivery, event, config = webhookFixture(broken.URL)
	outcome = (HTTPSender{}).sendWebhook(context.Background(), delivery, event, config.Webhook)
	if outcome.State != DeliveryUncertain || !outcome.Wrote || requests.Load() != 1 {
		t.Fatalf("disconnect outcome=%+v requests=%d", outcome, requests.Load())
	}
}

func TestWebhookConnectFailureIsTheOnlyRetryableClass(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	delivery, event, config := webhookFixture("http://" + address + "/notify")
	outcome := (HTTPSender{}).sendWebhook(context.Background(), delivery, event, config.Webhook)
	if !outcome.Retryable || outcome.Wrote || outcome.Code != "notification_connect_failed" {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestNotificationTransportDisablesTransparentConnectionReuseAndHTTP2(t *testing.T) {
	transport, err := notificationTransport("", "", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !transport.DisableKeepAlives || transport.ForceAttemptHTTP2 || transport.TLSNextProto == nil {
		t.Fatalf("unsafe transport=%+v", transport)
	}
}

func TestCustomWebhookJSONRendersStructuredValuesWithoutBreakingEscapes(t *testing.T) {
	delivery, event, config := webhookFixture("https://example.com/notify")
	event.Title = `title "quoted"`
	event.Text = "line one\nline two"
	config.Webhook.Format = "custom"
	config.Webhook.BodyMode = "json"
	config.Webhook.PayloadTemplate = `{"title":"{{title}}","nested":{"text":"prefix {{text}}"},"fixed":true}`
	request, err := buildWebhookRequest(context.Background(), delivery, event, config.Webhook)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if json.Unmarshal(body, &decoded) != nil || decoded["title"] != event.Title || decoded["fixed"] != true {
		t.Fatalf("body=%s decoded=%+v", body, decoded)
	}
	nested, _ := decoded["nested"].(map[string]any)
	if nested["text"] != "prefix "+event.Text {
		t.Fatalf("nested=%+v", nested)
	}
}

func TestGenericWebhookAndHumanChannelsRetainLegacyLineIdentityFields(t *testing.T) {
	delivery, event, config := webhookFixture("https://example.com/notify")
	event.LineName, event.CardID, event.MSISDN = "UK line", "8944100000000000001", "+441234"
	request, err := buildWebhookRequest(context.Background(), delivery, event, config.Webhook)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(request.Body)
	var fields map[string]string
	if json.Unmarshal(body, &fields) != nil || fields["instance"] != event.LineID || fields["sim_name"] != event.LineName ||
		fields["iccid"] != event.CardID || fields["msisdn"] != event.MSISDN || fields["from"] != event.Peer ||
		!strings.Contains(fields["content"], "来源号码: +100") {
		t.Fatalf("fields=%+v", fields)
	}
	content := notificationContent(event)
	for _, wanted := range []string{"SIM: UK line", "本机号码: +441234", "来源号码: +100", "hello"} {
		if !strings.Contains(content, wanted) {
			t.Fatalf("content=%q missing=%q", content, wanted)
		}
	}
}

func TestWebhookEventPlaceholdersCannotChangeDestinationAuthority(t *testing.T) {
	delivery, event, config := webhookFixture("https://{{from}}@example.com/notify")
	if _, err := buildWebhookRequest(context.Background(), delivery, event, config.Webhook); err == nil {
		t.Fatal("event-controlled webhook authority was accepted")
	}
}

func TestVendorResponsesRequireExplicitOfficialOutcome(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome Outcome
		state   string
	}{
		{name: "telegram success", outcome: classifyTelegramResponse(200, []byte(`{"ok":true}`)), state: DeliveryDelivered},
		{name: "telegram reject", outcome: classifyTelegramResponse(400, []byte(`{"ok":false}`)), state: DeliveryFailed},
		{name: "telegram missing", outcome: classifyTelegramResponse(200, []byte(`{}`)), state: DeliveryUncertain},
		{name: "push success number", outcome: classifyPushPlusResponse(200, []byte(`{"code":200}`)), state: DeliveryDelivered},
		{name: "push reject string", outcome: classifyPushPlusResponse(200, []byte(`{"code":"500"}`)), state: DeliveryFailed},
		{name: "push null", outcome: classifyPushPlusResponse(200, []byte(`{"code":null}`)), state: DeliveryUncertain},
		{name: "push missing", outcome: classifyPushPlusResponse(200, []byte(`{}`)), state: DeliveryUncertain},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.outcome.State != test.state || !test.outcome.Wrote {
				t.Fatalf("outcome=%+v", test.outcome)
			}
		})
	}
}

func TestTelegramTextIsBoundedWithoutSplittingUnicode(t *testing.T) {
	text := truncateRunes("前"+strings.Repeat("界", maximumTelegramLen)+"后", maximumTelegramLen)
	if len([]rune(text)) != maximumTelegramLen || []rune(text)[maximumTelegramLen-1] != '…' {
		t.Fatalf("runes=%d suffix=%q", len([]rune(text)), []rune(text)[maximumTelegramLen-1])
	}
}

func webhookFixture(endpoint string) (Delivery, Event, Config) {
	now := time.Unix(1_800_000_000, 0).UTC()
	config := DefaultConfig()
	config.Revision = 2
	config.Webhook.Enabled, config.Webhook.URL = true, endpoint
	event := Event{SchemaVersion: 1, EventID: "event-1", SourceID: "source-1", Kind: KindEvent,
		Type: EventIncomingSMS, LineID: "line-1", CardID: "12345678", Transport: "vowifi", Title: "SMS", Text: "hello",
		Peer: "+100", OccurredAt: now, IntakeRevision: 2, Targets: []string{ChannelWebhook}}
	delivery := Delivery{SchemaVersion: 1, DeliveryID: "delivery-1", EventID: event.EventID,
		Kind: KindEvent, EventType: event.Type, LineID: event.LineID, Channel: ChannelWebhook,
		ConfigRevision: 2, State: DeliverySending, Attempts: 1, NotBefore: now, CreatedAt: now, UpdatedAt: now}
	return delivery, event, config
}
