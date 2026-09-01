package notifications

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyImportIgnoresRemovedTelegramCommandsAndKeepsJSONTemplateSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	payload := []byte(`settings:
  timezone: Asia/Shanghai
  webhook:
    enabled: false
    format: universal_push
    method: POST
    body_mode: json
    url: https://example.com/notify
    headers_json: '{}'
    payload_template: ''
    verify_tls: true
    events:
      incoming_sms: true
    source: 'quoted " source'
    token: secret
  telegram:
    enabled: false
    bot_token: bot
    chat_id: chat
    proxy_mode: direct
    proxy_url: ''
    proxy_country: ''
    events:
      incoming_call: true
    commands:
      enabled: true
  pushplus:
    enabled: false
    token: push
    topic: topic
    template: html
    channel: wechat
    events:
      host_alert: true
`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadLegacy(path, time.Unix(1_800_000_000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.Webhook.Format != "custom" || !containsString(result.Proof.Warnings, "legacy_telegram_commands_ignored") ||
		!containsString(result.Proof.Warnings, "legacy_universal_push_converted") {
		t.Fatalf("config=%+v warnings=%v", result.Config.Webhook, result.Proof.Warnings)
	}
	if _, err := renderStructuredTemplate(result.Config.Webhook.PayloadTemplate, map[string]string{"title": `a"b`, "text": "x\ny", "content": "full\ncontent"}); err != nil {
		t.Fatalf("converted template is invalid: %v", err)
	}
}

func TestLegacyImportRejectsAStoreWhoseProducerBaselineAlreadyRan(t *testing.T) {
	store := openNotificationStore(t)
	if err := store.SeedReceipts(nil, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	proof := LegacyImportProof{SourceSHA256: strings.Repeat("a", 64), ImportedAt: now, Warnings: []string{}}
	config := DefaultConfig()
	config.Imported = &proof
	if _, _, err := store.ImportLegacy(LegacyResult{Config: config, Proof: proof}); !errors.Is(err, ErrConflict) {
		t.Fatalf("seeded store import err=%v", err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
