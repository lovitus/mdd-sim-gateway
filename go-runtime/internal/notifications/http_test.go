package notifications

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConfigHTTPNeverEchoesSecretsAndAppliesTriStatePatch(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	config, err := store.Config()
	if err != nil {
		t.Fatal(err)
	}
	config.Webhook.URL = "https://secret.example/notify"
	config.Webhook.Headers = map[string]string{"Authorization": "secret-header"}
	config.Webhook.PayloadTemplate = `{"secret":"secret-template"}`
	config.Telegram.BotToken, config.Telegram.ChatID = "secret-bot", "secret-chat"
	config.Telegram.ProxyURL = "socks5://secret-user:secret-pass@127.0.0.1:1080"
	config.PushPlus.Token, config.PushPlus.Topic = "secret-push", "secret-topic"
	config, _, err = store.PutConfigExpected(config.Revision, config, now)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store, func() time.Time { return now.Add(time.Second) }, func() {}, func() {})
	if err != nil {
		t.Fatal(err)
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/notifications/config", nil))
	for _, secret := range []string{"secret.example", "secret-header", "secret-template", "secret-bot", "secret-chat", "secret-user", "secret-pass", "secret-push", "secret-topic"} {
		if strings.Contains(get.Body.String(), secret) {
			t.Fatalf("GET echoed secret %q: %s", secret, get.Body.String())
		}
	}
	var view ConfigView
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &view) != nil ||
		!view.Webhook.URL.Configured || !view.Telegram.BotToken.Configured || !view.PushPlus.Token.Configured {
		t.Fatalf("status=%d view=%+v body=%s", get.Code, view, get.Body.String())
	}

	payload := configPatchPayload(view, map[string]any{})
	put := httptest.NewRecorder()
	handler.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/notifications/config", bytes.NewReader(payload)))
	kept, err := store.Config()
	if put.Code != http.StatusOK || err != nil || kept.Telegram.BotToken != "secret-bot" || kept.Webhook.URL != "https://secret.example/notify" {
		t.Fatalf("status=%d config=%+v err=%v body=%s", put.Code, kept, err, put.Body.String())
	}

	var nextView ConfigView
	if json.Unmarshal(put.Body.Bytes(), &nextView) != nil {
		t.Fatal("invalid PUT response")
	}
	payload = configPatchPayload(nextView, map[string]any{"bot_token": ""})
	put = httptest.NewRecorder()
	handler.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/notifications/config", bytes.NewReader(payload)))
	cleared, err := store.Config()
	if put.Code != http.StatusOK || err != nil || cleared.Telegram.BotToken != "" || cleared.Telegram.ChatID != "secret-chat" {
		t.Fatalf("status=%d config=%+v err=%v body=%s", put.Code, cleared, err, put.Body.String())
	}
}

func configPatchPayload(view ConfigView, telegramSecrets map[string]any) []byte {
	telegram := map[string]any{
		"enabled": view.Telegram.Enabled, "events": view.Telegram.Events,
		"proxy_mode": view.Telegram.ProxyMode, "proxy_country": view.Telegram.ProxyCountry,
	}
	for key, value := range telegramSecrets {
		telegram[key] = value
	}
	payload := map[string]any{
		"expected_revision": view.Revision, "timezone": view.Timezone,
		"webhook": map[string]any{
			"enabled": view.Webhook.Enabled, "events": view.Webhook.Events, "format": view.Webhook.Format,
			"method": view.Webhook.Method, "body_mode": view.Webhook.BodyMode, "tls_cert_sha256": view.Webhook.TLSCertSHA256,
		},
		"telegram": telegram,
		"pushplus": map[string]any{
			"enabled": view.PushPlus.Enabled, "events": view.PushPlus.Events,
			"template": view.PushPlus.Template, "channel": view.PushPlus.Channel,
		},
	}
	wire, _ := json.Marshal(payload)
	return wire
}
