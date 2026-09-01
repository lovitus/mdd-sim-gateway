// Package notifications owns durable, bounded outbound notification config,
// event intake and delivery receipts. It never owns call, SMS, hardware,
// Provider or recovery state.
package notifications

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = 1

const (
	EventIncomingSMS        = "incoming_sms"
	EventIncomingCall       = "incoming_call"
	EventHostAlert          = "host_alert"
	EventActivationReminder = "activation_reminder"
	EventTest               = "test"

	ChannelWebhook  = "webhook"
	ChannelTelegram = "telegram"
	ChannelPushPlus = "pushplus"

	DeliveryPending   = "pending"
	DeliverySending   = "sending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
	DeliveryUncertain = "uncertain"
	DeliveryCanceled  = "canceled"

	KindEvent = "event"
	KindTest  = "test"
)

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Subscriptions struct {
	IncomingSMS        bool `json:"incoming_sms"`
	IncomingCall       bool `json:"incoming_call"`
	HostAlert          bool `json:"host_alert"`
	ActivationReminder bool `json:"activation_reminder"`
}

func defaultSubscriptions() Subscriptions {
	return Subscriptions{IncomingSMS: true, IncomingCall: true, HostAlert: true, ActivationReminder: true}
}

func (subscriptions Subscriptions) Enabled(event string) bool {
	switch event {
	case EventIncomingSMS:
		return subscriptions.IncomingSMS
	case EventIncomingCall:
		return subscriptions.IncomingCall
	case EventHostAlert:
		return subscriptions.HostAlert
	case EventActivationReminder:
		return subscriptions.ActivationReminder
	default:
		return false
	}
}

type WebhookConfig struct {
	Enabled         bool              `json:"enabled"`
	Events          Subscriptions     `json:"events"`
	Format          string            `json:"format"`
	Method          string            `json:"method"`
	BodyMode        string            `json:"body_mode"`
	URL             string            `json:"url,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	PayloadTemplate string            `json:"payload_template,omitempty"`
	TLSCertSHA256   string            `json:"tls_cert_sha256,omitempty"`
	ConfiguredAt    time.Time         `json:"configured_at,omitempty"`
}

type TelegramConfig struct {
	Enabled      bool          `json:"enabled"`
	Events       Subscriptions `json:"events"`
	BotToken     string        `json:"bot_token,omitempty"`
	ChatID       string        `json:"chat_id,omitempty"`
	ProxyMode    string        `json:"proxy_mode"`
	ProxyURL     string        `json:"proxy_url,omitempty"`
	ProxyCountry string        `json:"proxy_country,omitempty"`
	ConfiguredAt time.Time     `json:"configured_at,omitempty"`
}

type PushPlusConfig struct {
	Enabled      bool          `json:"enabled"`
	Events       Subscriptions `json:"events"`
	Token        string        `json:"token,omitempty"`
	Topic        string        `json:"topic,omitempty"`
	Template     string        `json:"template"`
	Channel      string        `json:"channel"`
	ConfiguredAt time.Time     `json:"configured_at,omitempty"`
}

type Config struct {
	SchemaVersion int                `json:"schema_version"`
	Revision      uint64             `json:"revision"`
	Timezone      string             `json:"timezone"`
	Webhook       WebhookConfig      `json:"webhook"`
	Telegram      TelegramConfig     `json:"telegram"`
	PushPlus      PushPlusConfig     `json:"pushplus"`
	Imported      *LegacyImportProof `json:"imported,omitempty"`
}

type LegacyImportProof struct {
	SourceSHA256 string    `json:"source_sha256"`
	ImportedAt   time.Time `json:"imported_at"`
	Warnings     []string  `json:"warnings"`
}

func DefaultConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion, Revision: 1, Timezone: "Asia/Shanghai",
		Webhook:  WebhookConfig{Events: defaultSubscriptions(), Format: "generic", Method: http.MethodPost, BodyMode: "json"},
		Telegram: TelegramConfig{Events: defaultSubscriptions(), ProxyMode: "direct"},
		PushPlus: PushPlusConfig{Events: defaultSubscriptions(), Template: "html", Channel: "wechat"},
	}
}

func (config Config) Validate() error {
	if config.SchemaVersion != SchemaVersion || config.Revision == 0 {
		return errors.New("invalid notification config identity")
	}
	if _, err := time.LoadLocation(strings.TrimSpace(config.Timezone)); err != nil {
		return errors.New("invalid notification timezone")
	}
	if err := config.Webhook.validate(); err != nil {
		return err
	}
	if err := config.Telegram.validate(); err != nil {
		return err
	}
	if err := config.PushPlus.validate(); err != nil {
		return err
	}
	if config.Imported != nil {
		if !hexSHA256.MatchString(config.Imported.SourceSHA256) || config.Imported.ImportedAt.IsZero() || len(config.Imported.Warnings) > 16 {
			return errors.New("invalid notification import proof")
		}
		for _, warning := range config.Imported.Warnings {
			if !machineCode(warning) {
				return errors.New("invalid notification import warning")
			}
		}
	}
	return nil
}

func (config WebhookConfig) validate() error {
	if config.Format != "generic" && config.Format != "custom" ||
		config.Method != http.MethodGet && config.Method != http.MethodPost ||
		config.BodyMode != "json" && config.BodyMode != "form" && config.BodyMode != "raw" ||
		len(config.URL) > 4096 || len(config.PayloadTemplate) > 32<<10 || len(config.Headers) > 32 {
		return errors.New("invalid webhook notification config")
	}
	if config.URL != "" {
		parsed, err := url.Parse(config.URL)
		if err != nil || parsed.Host == "" || parsed.Fragment != "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return errors.New("invalid webhook URL")
		}
		if config.TLSCertSHA256 != "" && parsed.Scheme != "https" {
			return errors.New("webhook TLS pin requires HTTPS")
		}
	}
	if config.TLSCertSHA256 != "" && !hexSHA256.MatchString(config.TLSCertSHA256) {
		return errors.New("invalid webhook TLS certificate pin")
	}
	if config.Enabled && config.URL == "" {
		return errors.New("enabled webhook requires a URL")
	}
	if config.Format == "custom" && config.PayloadTemplate == "" {
		return errors.New("custom webhook requires a payload template")
	}
	if config.Format == "custom" && config.BodyMode != "raw" {
		if !json.Valid([]byte(config.PayloadTemplate)) {
			return errors.New("custom webhook template must be valid JSON")
		}
		if config.BodyMode == "form" {
			var fields map[string]any
			if json.Unmarshal([]byte(config.PayloadTemplate), &fields) != nil || fields == nil {
				return errors.New("custom form webhook template must be a JSON object")
			}
		}
	}
	for name, value := range config.Headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonical == "" || canonical != name || reservedHeader(canonical) || len(value) > 4096 || hasControl(value) {
			return errors.New("invalid webhook header")
		}
	}
	return nil
}

func (config TelegramConfig) validate() error {
	if len(config.BotToken) > 512 || len(config.ChatID) > 256 || len(config.ProxyURL) > 4096 ||
		config.ProxyMode != "direct" && config.ProxyMode != "manual" && config.ProxyMode != "country" {
		return errors.New("invalid Telegram notification config")
	}
	if config.ProxyMode == "manual" {
		parsed, err := url.Parse(config.ProxyURL)
		if err != nil || parsed.Host == "" || parsed.Fragment != "" ||
			parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5" && parsed.Scheme != "socks5h" {
			return errors.New("invalid Telegram proxy URL")
		}
	}
	if config.ProxyMode == "country" && !countryCode(config.ProxyCountry) {
		return errors.New("invalid Telegram proxy country")
	}
	if config.Enabled && (strings.TrimSpace(config.BotToken) == "" || strings.TrimSpace(config.ChatID) == "") {
		return errors.New("enabled Telegram requires bot token and chat ID")
	}
	return nil
}

func (config PushPlusConfig) validate() error {
	if len(config.Token) > 512 || len(config.Topic) > 512 ||
		!oneOf(config.Template, "html", "txt", "markdown", "json") ||
		!oneOf(config.Channel, "wechat", "app", "mail", "webhook", "cp", "clawbot") {
		return errors.New("invalid PushPlus notification config")
	}
	if config.Enabled && strings.TrimSpace(config.Token) == "" {
		return errors.New("enabled PushPlus requires a token")
	}
	return nil
}

func (config Config) ChannelEnabled(channel, event string) bool {
	switch channel {
	case ChannelWebhook:
		return config.Webhook.Enabled && (event == EventTest || config.Webhook.Events.Enabled(event))
	case ChannelTelegram:
		return config.Telegram.Enabled && (event == EventTest || config.Telegram.Events.Enabled(event))
	case ChannelPushPlus:
		return config.PushPlus.Enabled && (event == EventTest || config.PushPlus.Events.Enabled(event))
	default:
		return false
	}
}

func (config Config) Targets(event string) []string {
	targets := make([]string, 0, 3)
	for _, channel := range []string{ChannelWebhook, ChannelTelegram, ChannelPushPlus} {
		if config.ChannelEnabled(channel, event) {
			targets = append(targets, channel)
		}
	}
	return targets
}

type Event struct {
	SchemaVersion  int            `json:"schema_version"`
	EventID        string         `json:"event_id"`
	SourceID       string         `json:"source_id"`
	Kind           string         `json:"kind"`
	Type           string         `json:"type"`
	LineID         string         `json:"line_id,omitempty"`
	LineName       string         `json:"line_name,omitempty"`
	CardID         string         `json:"card_id,omitempty"`
	MSISDN         string         `json:"msisdn,omitempty"`
	Transport      string         `json:"transport,omitempty"`
	Title          string         `json:"title,omitempty"`
	Text           string         `json:"text,omitempty"`
	Peer           string         `json:"peer,omitempty"`
	OccurredAt     time.Time      `json:"occurred_at"`
	IntakeRevision uint64         `json:"intake_config_revision"`
	Targets        []string       `json:"targets"`
	Reminder       *ReminderFence `json:"reminder,omitempty"`
	PayloadCleared bool           `json:"payload_cleared,omitempty"`
}

type ReminderFence struct {
	ExpectedCardID    string `json:"expected_card_id"`
	ValidUntil        string `json:"valid_until"`
	Timezone          string `json:"timezone"`
	DaysBeforeExpiry  int    `json:"days_before_expiry"`
	AllowanceRevision uint64 `json:"allowance_revision"`
}

type HostAlertInput struct {
	Key      string
	Code     string
	Scope    string
	Severity string
	Title    string
	Text     string
}

func (alert HostAlertInput) Validate() error {
	if !identifier(alert.Key, 256) || !machineCode(alert.Code) || !identifier(alert.Scope, 256) ||
		!oneOf(alert.Severity, "warning", "critical") || len(alert.Title) > 1024 || len(alert.Text) > 8192 {
		return errors.New("invalid host notification alert")
	}
	return nil
}

func (event Event) Validate() error {
	if event.SchemaVersion != SchemaVersion || !identifier(event.EventID, 200) || !identifier(event.SourceID, 256) ||
		!oneOf(event.Kind, KindEvent, KindTest) || !oneOf(event.Type, EventIncomingSMS, EventIncomingCall,
		EventHostAlert, EventActivationReminder, EventTest) || event.OccurredAt.IsZero() || event.IntakeRevision == 0 ||
		len(event.Targets) > 3 || len(event.LineName) > 256 || len(event.MSISDN) > 128 ||
		len(event.Title) > 1024 || len(event.Text) > 32<<10 || len(event.Peer) > 512 {
		return errors.New("invalid notification event")
	}
	seen := map[string]bool{}
	for _, target := range event.Targets {
		if !oneOf(target, ChannelWebhook, ChannelTelegram, ChannelPushPlus) || seen[target] {
			return errors.New("invalid notification targets")
		}
		seen[target] = true
	}
	switch event.Type {
	case EventIncomingSMS, EventIncomingCall:
		if event.Kind != KindEvent || !identifier(event.LineID, 128) || (!event.PayloadCleared && !cardID(event.CardID)) ||
			event.Transport != "vowifi" || event.Reminder != nil {
			return errors.New("invalid realtime notification event")
		}
	case EventHostAlert:
		if event.Kind != KindEvent || event.Reminder != nil {
			return errors.New("invalid host notification event")
		}
	case EventActivationReminder:
		if event.Kind != KindEvent {
			return errors.New("invalid activation reminder event")
		}
	case EventTest:
		if event.Kind != KindTest || len(event.Targets) != 1 || event.Reminder != nil {
			return errors.New("invalid notification test event")
		}
	}
	if event.Type == EventActivationReminder && !event.PayloadCleared {
		if event.Kind != KindEvent || event.Reminder == nil || !identifier(event.LineID, 128) || !cardID(event.CardID) ||
			event.Reminder.ExpectedCardID != event.CardID || !isoDate(event.Reminder.ValidUntil) ||
			!oneOfInt(event.Reminder.DaysBeforeExpiry, 1, 2, 3) || event.Reminder.AllowanceRevision == 0 {
			return errors.New("invalid activation reminder fence")
		}
	}
	return nil
}

type Delivery struct {
	SchemaVersion  int       `json:"schema_version"`
	DeliveryID     string    `json:"delivery_id"`
	EventID        string    `json:"event_id"`
	OperationID    string    `json:"operation_id,omitempty"`
	Kind           string    `json:"kind"`
	EventType      string    `json:"event_type"`
	LineID         string    `json:"line_id,omitempty"`
	Channel        string    `json:"channel"`
	ConfigRevision uint64    `json:"config_revision"`
	State          string    `json:"state"`
	Attempts       int       `json:"attempts"`
	NotBefore      time.Time `json:"not_before"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	HTTPStatus     int       `json:"http_status,omitempty"`
	Code           string    `json:"code,omitempty"`
}

func (delivery Delivery) Validate() error {
	if delivery.SchemaVersion != SchemaVersion || !identifier(delivery.DeliveryID, 200) ||
		!identifier(delivery.EventID, 200) || !oneOf(delivery.Kind, KindEvent, KindTest) ||
		!oneOf(delivery.Channel, ChannelWebhook, ChannelTelegram, ChannelPushPlus) ||
		!oneOf(delivery.State, DeliveryPending, DeliverySending, DeliveryDelivered, DeliveryFailed,
			DeliveryUncertain, DeliveryCanceled) || delivery.ConfigRevision == 0 || delivery.Attempts < 0 ||
		delivery.Attempts > 3 || delivery.NotBefore.IsZero() || delivery.CreatedAt.IsZero() || delivery.UpdatedAt.IsZero() ||
		delivery.HTTPStatus < 0 || delivery.HTTPStatus > 999 || delivery.Code != "" && !machineCode(delivery.Code) {
		return errors.New("invalid notification delivery")
	}
	if delivery.Kind == KindTest {
		if delivery.EventType != EventTest || !identifier(delivery.OperationID, 200) {
			return errors.New("invalid notification test delivery")
		}
	} else if !validEventType(delivery.EventType) || delivery.OperationID != "" {
		return errors.New("invalid notification event delivery")
	}
	if delivery.Terminal() {
		if delivery.FinishedAt.IsZero() || !machineCode(delivery.Code) {
			return errors.New("invalid terminal notification delivery")
		}
	} else if !delivery.FinishedAt.IsZero() || delivery.Code != "" {
		return errors.New("invalid active notification delivery")
	}
	return nil
}

func (delivery Delivery) Terminal() bool {
	return oneOf(delivery.State, DeliveryDelivered, DeliveryFailed, DeliveryUncertain, DeliveryCanceled)
}

func validEventType(value string) bool {
	return oneOf(value, EventIncomingSMS, EventIncomingCall, EventHostAlert, EventActivationReminder)
}

func identifier(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maximum || hasControl(value) {
		return false
	}
	return true
}

func machineCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func hasControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func reservedHeader(value string) bool {
	switch strings.ToLower(value) {
	case "host", "connection", "content-length", "transfer-encoding", "upgrade",
		"idempotency-key", "x-idempotency-key", "x-mdd-event-id", "x-mdd-delivery-id":
		return true
	default:
		return false
	}
}

func countryCode(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return len(value) == 2 && value[0] >= 'a' && value[0] <= 'z' && value[1] >= 'a' && value[1] <= 'z'
}

func cardID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func isoDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	return err == nil && parsed.Format("2006-01-02") == strings.TrimSpace(value)
}

func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func oneOfInt(value int, options ...int) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func sortedTargets(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
