package notifications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	xproxy "golang.org/x/net/proxy"
)

const (
	deliveryTimeout    = 8 * time.Second
	maximumReplyBytes  = 32 << 10
	maximumTelegramLen = 4096
)

var notificationPlaceholder = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

type Outcome struct {
	State      string
	Code       string
	HTTPStatus int
	Retryable  bool
	Wrote      bool
}

type Sender interface {
	Send(context.Context, Delivery, Event, Config) Outcome
}

type HTTPSender struct {
	EgressStatusPath string
}

func (sender HTTPSender) Send(ctx context.Context, delivery Delivery, event Event, config Config) Outcome {
	switch delivery.Channel {
	case ChannelWebhook:
		return sender.sendWebhook(ctx, delivery, event, config.Webhook)
	case ChannelTelegram:
		return sender.sendTelegram(ctx, delivery, event, config.Telegram)
	case ChannelPushPlus:
		return sender.sendPushPlus(ctx, delivery, event, config.PushPlus)
	default:
		return Outcome{State: DeliveryFailed, Code: "notification_channel_invalid"}
	}
}

func (sender HTTPSender) sendWebhook(ctx context.Context, delivery Delivery, event Event, config WebhookConfig) Outcome {
	request, err := buildWebhookRequest(ctx, delivery, event, config)
	if err != nil {
		return Outcome{State: DeliveryFailed, Code: "webhook_request_invalid"}
	}
	return executeHTTP(request, "", config.TLSCertSHA256, func(status int, _ []byte) Outcome {
		if status >= 200 && status < 300 {
			return Outcome{State: DeliveryDelivered, Code: "notification_delivered", HTTPStatus: status, Wrote: true}
		}
		// A generic endpoint can perform the side effect and still return any
		// non-2xx status. Never claim it was definitely rejected or retry it.
		return Outcome{State: DeliveryUncertain, Code: "webhook_result_uncertain", HTTPStatus: status, Wrote: true}
	})
}

func (sender HTTPSender) sendTelegram(ctx context.Context, delivery Delivery, event Event, config TelegramConfig) Outcome {
	proxyURL := ""
	switch config.ProxyMode {
	case "direct":
	case "manual":
		proxyURL = config.ProxyURL
	case "country":
		snapshot, err := egressstatus.Load(sender.EgressStatusPath)
		if err != nil {
			return Outcome{State: DeliveryFailed, Code: "telegram_country_egress_unavailable", Retryable: true}
		}
		proxyURL, err = snapshot.ProxyURL(config.ProxyCountry)
		if err != nil {
			return Outcome{State: DeliveryFailed, Code: "telegram_country_egress_unavailable", Retryable: true}
		}
	default:
		return Outcome{State: DeliveryFailed, Code: "telegram_proxy_invalid"}
	}
	body, err := json.Marshal(map[string]any{
		"chat_id": config.ChatID, "text": truncateRunes(telegramText(event), maximumTelegramLen), "disable_web_page_preview": true,
	})
	if err != nil || len(body) > 16<<10 {
		return Outcome{State: DeliveryFailed, Code: "telegram_payload_invalid"}
	}
	endpoint := "https://api.telegram.org/bot" + url.PathEscape(config.BotToken) + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Outcome{State: DeliveryFailed, Code: "telegram_request_invalid"}
	}
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	return executeHTTP(request, proxyURL, "", classifyTelegramResponse)
}

func (sender HTTPSender) sendPushPlus(ctx context.Context, delivery Delivery, event Event, config PushPlusConfig) Outcome {
	body, err := json.Marshal(map[string]any{
		"token": config.Token, "title": event.Title, "content": notificationContent(event),
		"topic": config.Topic, "template": config.Template, "channel": config.Channel,
	})
	if err != nil || len(body) > 40<<10 {
		return Outcome{State: DeliveryFailed, Code: "pushplus_payload_invalid"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://www.pushplus.plus/send/", bytes.NewReader(body))
	if err != nil {
		return Outcome{State: DeliveryFailed, Code: "pushplus_request_invalid"}
	}
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	return executeHTTP(request, "", "", classifyPushPlusResponse)
}

func classifyTelegramResponse(status int, response []byte) Outcome {
	var result struct {
		OK *bool `json:"ok"`
	}
	valid := json.Unmarshal(response, &result) == nil && result.OK != nil
	if status >= 200 && status < 300 && valid && *result.OK {
		return Outcome{State: DeliveryDelivered, Code: "notification_delivered", HTTPStatus: status, Wrote: true}
	}
	if valid && !*result.OK {
		return Outcome{State: DeliveryFailed, Code: "telegram_rejected", HTTPStatus: status, Wrote: true}
	}
	return Outcome{State: DeliveryUncertain, Code: "telegram_result_uncertain", HTTPStatus: status, Wrote: true}
}

func classifyPushPlusResponse(status int, response []byte) Outcome {
	var result struct {
		Code json.RawMessage `json:"code"`
	}
	validJSON := json.Unmarshal(response, &result) == nil
	code, explicit := explicitAPICode(result.Code)
	if status >= 200 && status < 300 && validJSON && explicit && code == "200" {
		return Outcome{State: DeliveryDelivered, Code: "notification_delivered", HTTPStatus: status, Wrote: true}
	}
	if validJSON && explicit && code != "200" {
		return Outcome{State: DeliveryFailed, Code: "pushplus_rejected", HTTPStatus: status, Wrote: true}
	}
	return Outcome{State: DeliveryUncertain, Code: "pushplus_result_uncertain", HTTPStatus: status, Wrote: true}
}

func explicitAPICode(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text), true
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil && strings.TrimSpace(number.String()) != "" {
		return number.String(), true
	}
	return "", false
}

func executeHTTP(request *http.Request, proxyURL, tlsCertSHA256 string,
	classify func(int, []byte) Outcome) Outcome {
	transport, err := notificationTransport(proxyURL, tlsCertSHA256, request.URL.Hostname())
	if err != nil {
		return Outcome{State: DeliveryFailed, Code: "notification_transport_invalid"}
	}
	defer transport.CloseIdleConnections()
	var gotConnection, wroteHeaders, wroteRequest atomic.Bool
	trace := &httptrace.ClientTrace{
		GotConn:      func(httptrace.GotConnInfo) { gotConnection.Store(true) },
		WroteHeaders: func() { wroteHeaders.Store(true) },
		WroteRequest: func(httptrace.WroteRequestInfo) { wroteRequest.Store(true) },
	}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	client := &http.Client{
		Transport: transport, Timeout: deliveryTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	wrote := wroteHeaders.Load() || wroteRequest.Load()
	if err != nil {
		if wrote {
			return Outcome{State: DeliveryUncertain, Code: "notification_result_uncertain", Wrote: true}
		}
		code := "notification_connect_failed"
		if gotConnection.Load() {
			code = "notification_prewrite_failed"
		}
		return Outcome{State: DeliveryFailed, Code: code, Retryable: true}
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, maximumReplyBytes+1))
	if readErr != nil || len(payload) > maximumReplyBytes {
		return Outcome{State: DeliveryUncertain, Code: "notification_response_uncertain", HTTPStatus: response.StatusCode, Wrote: true}
	}
	outcome := classify(response.StatusCode, payload)
	outcome.Wrote = true
	return outcome
}

func notificationTransport(proxyURL, tlsCertSHA256, serverName string) (*http.Transport, error) {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}).DialContext,
		ForceAttemptHTTP2:     false,
		DisableKeepAlives:     true,
		MaxIdleConns:          0,
		IdleConnTimeout:       time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 0,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil || parsed.Host == "" {
			return nil, errors.New("invalid notification proxy")
		}
		switch parsed.Scheme {
		case "http", "https":
			transport.Proxy = http.ProxyURL(parsed)
		case "socks5", "socks5h":
			var auth *xproxy.Auth
			if parsed.User != nil {
				password, _ := parsed.User.Password()
				auth = &xproxy.Auth{User: parsed.User.Username(), Password: password}
			}
			dialer, err := xproxy.SOCKS5("tcp", parsed.Host, auth, &net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1})
			if err != nil {
				return nil, errors.New("invalid notification SOCKS proxy")
			}
			if contextual, ok := dialer.(xproxy.ContextDialer); ok {
				transport.DialContext = contextual.DialContext
			} else {
				transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
					return dialer.Dial(network, address)
				}
			}
		default:
			return nil, errors.New("invalid notification proxy scheme")
		}
	}
	if tlsCertSHA256 != "" {
		expected, err := hex.DecodeString(tlsCertSHA256)
		if err != nil || len(expected) != sha256.Size {
			return nil, errors.New("invalid notification TLS pin")
		}
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12, ServerName: serverName, InsecureSkipVerify: true, // exact pin and hostname are verified below
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("notification TLS peer has no certificate")
				}
				actual := sha256.Sum256(state.PeerCertificates[0].Raw)
				if !bytes.Equal(actual[:], expected) {
					return errors.New("notification TLS certificate pin mismatch")
				}
				if err := state.PeerCertificates[0].VerifyHostname(serverName); err != nil {
					return x509.HostnameError{Certificate: state.PeerCertificates[0], Host: serverName}
				}
				return nil
			},
		}
	}
	return transport, nil
}

func buildWebhookRequest(ctx context.Context, delivery Delivery, event Event, config WebhookConfig) (*http.Request, error) {
	fields := notificationFields(delivery, event)
	renderedURL := renderPlaceholders(config.URL, fields)
	configuredURL, configuredErr := url.Parse(config.URL)
	parsed, err := url.Parse(renderedURL)
	if configuredErr != nil || err != nil || parsed.Host == "" || parsed.Fragment != "" ||
		parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("invalid rendered webhook URL")
	}
	configuredUser, renderedUser := "", ""
	if configuredURL.User != nil {
		configuredUser = configuredURL.User.String()
	}
	if parsed.User != nil {
		renderedUser = parsed.User.String()
	}
	if parsed.Scheme != configuredURL.Scheme || parsed.Host != configuredURL.Host || renderedUser != configuredUser {
		return nil, errors.New("webhook placeholders cannot change the destination authority")
	}
	var payload any = fields
	rawPayload := false
	if config.Format == "custom" {
		if config.BodyMode == "raw" {
			payload, rawPayload = renderPlaceholders(config.PayloadTemplate, fields), true
		} else if payload, err = renderStructuredTemplate(config.PayloadTemplate, fields); err != nil {
			return nil, err
		}
	}
	body, contentType, err := encodeWebhookPayload(payload, config.BodyMode)
	if err != nil {
		return nil, err
	}
	if len(body) > 64<<10 {
		return nil, errors.New("webhook body is too large")
	}
	if config.Method == http.MethodGet {
		query := parsed.Query()
		if config.Format == "generic" {
			for key, value := range fields {
				query.Set(key, value)
			}
		} else if rawPayload {
			query.Set("payload", fmt.Sprint(payload))
		} else if values, ok := payload.(map[string]any); ok {
			for key, value := range values {
				query.Set(key, fmt.Sprint(value))
			}
		} else {
			return nil, errors.New("custom GET webhook payload must be an object or raw text")
		}
		parsed.RawQuery = query.Encode()
		body = nil
	}
	if len(parsed.String()) > 64<<10 {
		return nil, errors.New("rendered webhook URL is too large")
	}
	request, err := http.NewRequestWithContext(ctx, config.Method, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.GetBody = nil
	if config.Method == http.MethodPost {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Idempotency-Key", delivery.DeliveryID)
	request.Header.Set("X-MDD-Event-ID", event.EventID)
	request.Header.Set("X-MDD-Delivery-ID", delivery.DeliveryID)
	for name, value := range config.Headers {
		rendered := renderPlaceholders(value, fields)
		if len(rendered) > 4096 || hasControl(rendered) {
			return nil, errors.New("rendered webhook header is invalid")
		}
		request.Header.Set(name, rendered)
	}
	return request, nil
}

func notificationFields(delivery Delivery, event Event) map[string]string {
	return map[string]string{
		"delivery_id": delivery.DeliveryID, "event_id": event.EventID, "event": event.Type,
		"line_id": event.LineID, "instance": event.LineID, "line_name": event.LineName, "sim_name": event.LineName,
		"card_id": event.CardID, "iccid": event.CardID, "msisdn": event.MSISDN, "transport": event.Transport,
		"title": event.Title, "text": event.Text, "content": notificationContent(event), "from": event.Peer,
		"occurred_at": event.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
}

func renderPlaceholders(value string, fields map[string]string) string {
	return notificationPlaceholder.ReplaceAllStringFunc(value, func(token string) string {
		matches := notificationPlaceholder.FindStringSubmatch(token)
		if len(matches) != 2 {
			return ""
		}
		return fields[matches[1]]
	})
}

func renderStructuredTemplate(template string, fields map[string]string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(template))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("webhook payload template is not valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("webhook payload template has trailing JSON")
	}
	return renderTemplateValue(value, fields)
}

func renderTemplateValue(value any, fields map[string]string) (any, error) {
	switch typed := value.(type) {
	case string:
		return renderPlaceholders(typed, fields), nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			rendered, err := renderTemplateValue(item, fields)
			if err != nil {
				return nil, err
			}
			result[index] = rendered
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			renderedKey := renderPlaceholders(key, fields)
			if _, duplicate := result[renderedKey]; duplicate {
				return nil, errors.New("rendered webhook payload has duplicate keys")
			}
			rendered, err := renderTemplateValue(item, fields)
			if err != nil {
				return nil, err
			}
			result[renderedKey] = rendered
		}
		return result, nil
	default:
		return value, nil
	}
}

func encodeWebhookPayload(value any, mode string) ([]byte, string, error) {
	switch mode {
	case "json":
		body, err := json.Marshal(value)
		return body, "application/json", err
	case "form":
		values, ok := value.(map[string]any)
		if !ok {
			return nil, "", errors.New("form webhook payload must be an object")
		}
		form := url.Values{}
		for key, item := range values {
			form.Set(key, fmt.Sprint(item))
		}
		return []byte(form.Encode()), "application/x-www-form-urlencoded", nil
	case "raw":
		if text, ok := value.(string); ok {
			return []byte(text), "text/plain; charset=utf-8", nil
		}
		body, err := json.Marshal(value)
		return body, "application/json", err
	default:
		return nil, "", errors.New("unsupported webhook body mode")
	}
}

func telegramText(event Event) string {
	switch event.Type {
	case EventIncomingSMS:
		return strings.TrimSpace("📩 " + event.Title + "\n" + notificationContent(event))
	case EventIncomingCall:
		return strings.TrimSpace("📞 " + event.Title + "\n" + notificationContent(event))
	case EventHostAlert:
		return strings.TrimSpace("⚠️ MDD 主机告警\n" + event.Title + "\n" + event.Text)
	case EventActivationReminder:
		return strings.TrimSpace("⏰ SIM 即将到期\n" + event.Title + "\n" + event.Text)
	default:
		return strings.TrimSpace(event.Title + "\n" + event.Text)
	}
}

func notificationContent(event Event) string {
	if event.Type != EventIncomingSMS && event.Type != EventIncomingCall {
		return event.Text
	}
	lines := []string{"SIM: " + fallback(event.LineName, fallback(event.CardID, fallback(event.LineID, "SIM")))}
	if strings.TrimSpace(event.MSISDN) != "" {
		lines = append(lines, "本机号码: "+event.MSISDN)
	}
	lines = append(lines, "来源号码: "+fallback(event.Peer, "unknown"))
	if event.Type == EventIncomingSMS {
		lines = append(lines, "", event.Text)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func fallback(value, replacement string) string {
	if strings.TrimSpace(value) == "" {
		return replacement
	}
	return value
}

func truncateRunes(value string, maximum int) string {
	runes := []rune(value)
	if maximum < 1 || len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum-1]) + "…"
}
