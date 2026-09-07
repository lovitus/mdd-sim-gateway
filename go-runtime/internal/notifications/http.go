package notifications

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maximumNotificationRequestBytes = 128 << 10

type Handler struct {
	store         *Store
	now           func() time.Time
	configChanged func()
	wake          func()
}

func NewHandler(store *Store, now func() time.Time, configChanged, wake func()) (*Handler, error) {
	if store == nil || now == nil || configChanged == nil || wake == nil {
		return nil, errors.New("invalid notification HTTP configuration")
	}
	return &Handler{store: store, now: now, configChanged: configChanged, wake: wake}, nil
}

type SecretView struct {
	Configured bool `json:"configured"`
}

type WebhookView struct {
	Enabled         bool          `json:"enabled"`
	Events          Subscriptions `json:"events"`
	Format          string        `json:"format"`
	Method          string        `json:"method"`
	BodyMode        string        `json:"body_mode"`
	URL             SecretView    `json:"url"`
	Headers         SecretView    `json:"headers"`
	PayloadTemplate SecretView    `json:"payload_template"`
	TLSCertSHA256   string        `json:"tls_cert_sha256,omitempty"`
}

type TelegramView struct {
	Enabled      bool          `json:"enabled"`
	Events       Subscriptions `json:"events"`
	ProxyMode    string        `json:"proxy_mode"`
	ProxyCountry string        `json:"proxy_country,omitempty"`
	BotToken     SecretView    `json:"bot_token"`
	ChatID       SecretView    `json:"chat_id"`
	ProxyURL     SecretView    `json:"proxy_url"`
}

type PushPlusView struct {
	Enabled  bool          `json:"enabled"`
	Events   Subscriptions `json:"events"`
	Template string        `json:"template"`
	Channel  string        `json:"channel"`
	Token    SecretView    `json:"token"`
	Topic    SecretView    `json:"topic"`
}

type ConfigView struct {
	SchemaVersion      int               `json:"schema_version"`
	Revision           uint64            `json:"revision"`
	Timezone           string            `json:"timezone"`
	Webhook            WebhookView       `json:"webhook"`
	Telegram           TelegramView      `json:"telegram"`
	PushPlus           PushPlusView      `json:"pushplus"`
	SupportedEvents    []string          `json:"supported_events"`
	UnsupportedReasons map[string]string `json:"unsupported_reasons"`
}

type SecretPatch struct {
	Set   bool
	Value string
}

func (patch *SecretPatch) UnmarshalJSON(payload []byte) error {
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return errors.New("secret patch cannot be null")
	}
	var value string
	if json.Unmarshal(payload, &value) != nil {
		return errors.New("secret patch must be a string")
	}
	patch.Set, patch.Value = true, value
	return nil
}

type webhookPatch struct {
	Enabled         bool          `json:"enabled"`
	Events          Subscriptions `json:"events"`
	Format          string        `json:"format"`
	Method          string        `json:"method"`
	BodyMode        string        `json:"body_mode"`
	TLSCertSHA256   string        `json:"tls_cert_sha256"`
	URL             SecretPatch   `json:"url"`
	HeadersJSON     SecretPatch   `json:"headers_json"`
	PayloadTemplate SecretPatch   `json:"payload_template"`
}

type telegramPatch struct {
	Enabled      bool          `json:"enabled"`
	Events       Subscriptions `json:"events"`
	ProxyMode    string        `json:"proxy_mode"`
	ProxyCountry string        `json:"proxy_country"`
	BotToken     SecretPatch   `json:"bot_token"`
	ChatID       SecretPatch   `json:"chat_id"`
	ProxyURL     SecretPatch   `json:"proxy_url"`
}

type pushPlusPatch struct {
	Enabled  bool          `json:"enabled"`
	Events   Subscriptions `json:"events"`
	Template string        `json:"template"`
	Channel  string        `json:"channel"`
	Token    SecretPatch   `json:"token"`
	Topic    SecretPatch   `json:"topic"`
}

type configPatch struct {
	ExpectedRevision uint64         `json:"expected_revision"`
	Timezone         string         `json:"timezone"`
	Webhook          *webhookPatch  `json:"webhook"`
	Telegram         *telegramPatch `json:"telegram"`
	PushPlus         *pushPlusPatch `json:"pushplus"`
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/v1/system/alerts" && request.Method == http.MethodGet || request.URL.Path == "/v1/system/alerts/acknowledge" && request.Method == http.MethodPost {
		handler.hostAlerts(response, request)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	path := request.URL.Path
	switch {
	case path == "/v1/notifications/config":
		handler.config(response, request)
	case path == "/v1/notifications/deliveries":
		handler.deliveries(response, request)
	case strings.HasPrefix(path, "/v1/notifications/tests/"):
		handler.test(response, request, strings.TrimPrefix(path, "/v1/notifications/tests/"))
	default:
		http.NotFound(response, request)
	}
}

func (handler *Handler) config(response http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeNotificationError(response, http.StatusBadRequest, "invalid_notification_config_request")
		return
	}
	switch request.Method {
	case http.MethodGet:
		config, err := handler.store.Config()
		if err != nil {
			writeNotificationError(response, http.StatusInternalServerError, "notification_config_read_failed")
			return
		}
		writeNotificationJSON(response, http.StatusOK, configView(config))
	case http.MethodPut:
		var patch configPatch
		if decodeNotificationRequest(request.Body, &patch) != nil || patch.ExpectedRevision == 0 ||
			patch.Webhook == nil || patch.Telegram == nil || patch.PushPlus == nil {
			writeNotificationError(response, http.StatusBadRequest, "invalid_notification_config_request")
			return
		}
		current, err := handler.store.Config()
		if err != nil {
			writeNotificationError(response, http.StatusInternalServerError, "notification_config_read_failed")
			return
		}
		next, err := applyConfigPatch(current, patch)
		if err != nil {
			writeNotificationError(response, http.StatusBadRequest, "invalid_notification_config")
			return
		}
		updated, changed, err := handler.store.PutConfigExpected(patch.ExpectedRevision, next, handler.now().UTC())
		switch {
		case errors.Is(err, ErrRevision):
			writeNotificationError(response, http.StatusPreconditionFailed, "notification_config_revision_changed")
		case err != nil:
			writeNotificationError(response, http.StatusInternalServerError, "notification_config_write_failed")
		default:
			if changed {
				handler.configChanged()
			}
			writeNotificationJSON(response, http.StatusOK, configView(updated))
		}
	default:
		writeNotificationError(response, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (handler *Handler) deliveries(response http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeNotificationError(response, http.StatusBadRequest, "invalid_notification_delivery_request")
		return
	}
	switch request.Method {
	case http.MethodGet:
		deliveries, err := handler.store.Deliveries(200)
		if err != nil {
			writeNotificationError(response, http.StatusInternalServerError, "notification_delivery_read_failed")
			return
		}
		writeNotificationJSON(response, http.StatusOK, map[string]any{"deliveries": deliveries})
	case http.MethodDelete:
		if decodeNotificationRequest(request.Body, &struct{}{}) != nil {
			writeNotificationError(response, http.StatusBadRequest, "invalid_notification_delivery_request")
			return
		}
		deleted, err := handler.store.ClearTerminal()
		if err != nil {
			writeNotificationError(response, http.StatusInternalServerError, "notification_delivery_clear_failed")
			return
		}
		writeNotificationJSON(response, http.StatusOK, map[string]any{"deleted": deleted})
	default:
		writeNotificationError(response, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (handler *Handler) test(response http.ResponseWriter, request *http.Request, channel string) {
	if request.Method != http.MethodPost {
		writeNotificationError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if request.URL.RawQuery != "" || !oneOf(channel, ChannelWebhook, ChannelTelegram, ChannelPushPlus) {
		writeNotificationError(response, http.StatusBadRequest, "invalid_notification_test")
		return
	}
	var input struct {
		OperationID string `json:"operation_id"`
	}
	if decodeNotificationRequest(request.Body, &input) != nil || !identifier(input.OperationID, 200) {
		writeNotificationError(response, http.StatusBadRequest, "invalid_notification_test")
		return
	}
	delivery, created, err := handler.store.EnqueueTest(input.OperationID, channel, handler.now().UTC())
	switch {
	case errors.Is(err, ErrConflict):
		writeNotificationError(response, http.StatusConflict, "notification_test_conflict")
	case err != nil:
		writeNotificationError(response, http.StatusPreconditionFailed, "notification_test_unavailable")
	default:
		handler.wake()
		writeNotificationJSON(response, http.StatusAccepted, map[string]any{"created": created, "delivery": delivery})
	}
}

func configView(config Config) ConfigView {
	return ConfigView{
		SchemaVersion: SchemaVersion, Revision: config.Revision, Timezone: config.Timezone,
		Webhook: WebhookView{Enabled: config.Webhook.Enabled, Events: config.Webhook.Events,
			Format: config.Webhook.Format, Method: config.Webhook.Method, BodyMode: config.Webhook.BodyMode,
			URL: SecretView{Configured: config.Webhook.URL != ""}, Headers: SecretView{Configured: len(config.Webhook.Headers) > 0},
			PayloadTemplate: SecretView{Configured: config.Webhook.PayloadTemplate != ""}, TLSCertSHA256: config.Webhook.TLSCertSHA256},
		Telegram: TelegramView{Enabled: config.Telegram.Enabled, Events: config.Telegram.Events,
			ProxyMode: config.Telegram.ProxyMode, ProxyCountry: config.Telegram.ProxyCountry,
			BotToken: SecretView{Configured: config.Telegram.BotToken != ""}, ChatID: SecretView{Configured: config.Telegram.ChatID != ""},
			ProxyURL: SecretView{Configured: config.Telegram.ProxyURL != ""}},
		PushPlus: PushPlusView{Enabled: config.PushPlus.Enabled, Events: config.PushPlus.Events,
			Template: config.PushPlus.Template, Channel: config.PushPlus.Channel,
			Token: SecretView{Configured: config.PushPlus.Token != ""}, Topic: SecretView{Configured: config.PushPlus.Topic != ""}},
		SupportedEvents: []string{EventIncomingSMS, EventIncomingCall, EventHostAlert, EventActivationReminder},
		UnsupportedReasons: map[string]string{
			"number_changed":     "no_authoritative_ims_number_source",
			"line_unrecoverable": "continuous_recovery_has_no_terminal_unrecoverable_state",
		},
	}
}

func applyConfigPatch(current Config, patch configPatch) (Config, error) {
	next := cloneConfig(current)
	next.Timezone = strings.TrimSpace(patch.Timezone)
	webhook, telegram, pushplus := *patch.Webhook, *patch.Telegram, *patch.PushPlus
	next.Webhook.Enabled, next.Webhook.Events = webhook.Enabled, webhook.Events
	next.Webhook.Format, next.Webhook.Method, next.Webhook.BodyMode =
		strings.TrimSpace(webhook.Format), strings.ToUpper(strings.TrimSpace(webhook.Method)), strings.TrimSpace(webhook.BodyMode)
	next.Webhook.TLSCertSHA256 = strings.ToLower(strings.TrimSpace(webhook.TLSCertSHA256))
	if webhook.URL.Set {
		next.Webhook.URL = strings.TrimSpace(webhook.URL.Value)
	}
	if webhook.PayloadTemplate.Set {
		next.Webhook.PayloadTemplate = webhook.PayloadTemplate.Value
	}
	if webhook.HeadersJSON.Set {
		value := strings.TrimSpace(webhook.HeadersJSON.Value)
		if value == "" {
			next.Webhook.Headers = nil
		} else {
			var headers map[string]string
			decoder := json.NewDecoder(strings.NewReader(value))
			if decoder.Decode(&headers) != nil || len(headers) > 32 {
				return Config{}, errors.New("invalid webhook headers")
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				return Config{}, errors.New("invalid webhook headers")
			}
			normalized := make(map[string]string, len(headers))
			for name, value := range headers {
				canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
				if canonical == "" {
					return Config{}, errors.New("invalid webhook headers")
				}
				if _, duplicate := normalized[canonical]; duplicate {
					return Config{}, errors.New("duplicate webhook headers")
				}
				normalized[canonical] = value
			}
			next.Webhook.Headers = normalized
		}
	}
	next.Telegram.Enabled, next.Telegram.Events = telegram.Enabled, telegram.Events
	next.Telegram.ProxyMode = strings.TrimSpace(telegram.ProxyMode)
	next.Telegram.ProxyCountry = strings.ToLower(strings.TrimSpace(telegram.ProxyCountry))
	if telegram.BotToken.Set {
		next.Telegram.BotToken = strings.TrimSpace(telegram.BotToken.Value)
	}
	if telegram.ChatID.Set {
		next.Telegram.ChatID = strings.TrimSpace(telegram.ChatID.Value)
	}
	if telegram.ProxyURL.Set {
		next.Telegram.ProxyURL = strings.TrimSpace(telegram.ProxyURL.Value)
	}
	next.PushPlus.Enabled, next.PushPlus.Events = pushplus.Enabled, pushplus.Events
	next.PushPlus.Template, next.PushPlus.Channel = strings.TrimSpace(pushplus.Template), strings.TrimSpace(pushplus.Channel)
	if pushplus.Token.Set {
		next.PushPlus.Token = strings.TrimSpace(pushplus.Token.Value)
	}
	if pushplus.Topic.Set {
		next.PushPlus.Topic = strings.TrimSpace(pushplus.Topic.Value)
	}
	if err := next.Validate(); err != nil {
		return Config{}, err
	}
	return next, nil
}

func decodeNotificationRequest(body io.Reader, target any) error {
	payload, err := io.ReadAll(io.LimitReader(body, maximumNotificationRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumNotificationRequestBytes {
		return errors.New("invalid notification request size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("notification request has trailing JSON")
	}
	return nil
}

func writeNotificationJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeNotificationError(response http.ResponseWriter, status int, code string) {
	writeNotificationJSON(response, status, map[string]string{"code": code})
}

func sortedHeaderNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
