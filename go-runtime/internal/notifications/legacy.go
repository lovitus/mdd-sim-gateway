package notifications

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
	yaml "go.yaml.in/yaml/v3"
)

const maximumLegacyConfigBytes = 1 << 20

type legacyWebhook struct {
	Enabled         bool            `yaml:"enabled"`
	Format          string          `yaml:"format"`
	Method          string          `yaml:"method"`
	BodyMode        string          `yaml:"body_mode"`
	URL             string          `yaml:"url"`
	HeadersJSON     string          `yaml:"headers_json"`
	PayloadTemplate string          `yaml:"payload_template"`
	VerifyTLS       *bool           `yaml:"verify_tls"`
	Events          map[string]bool `yaml:"events"`
	Source          string          `yaml:"source"`
	Token           string          `yaml:"token"`
}

type legacyTelegram struct {
	Enabled      bool            `yaml:"enabled"`
	BotToken     string          `yaml:"bot_token"`
	ChatID       string          `yaml:"chat_id"`
	ProxyMode    string          `yaml:"proxy_mode"`
	ProxyURL     string          `yaml:"proxy_url"`
	ProxyCountry string          `yaml:"proxy_country"`
	Events       map[string]bool `yaml:"events"`
	Commands     yaml.Node       `yaml:"commands"`
}

type legacyPushPlus struct {
	Enabled  bool            `yaml:"enabled"`
	Token    string          `yaml:"token"`
	Topic    string          `yaml:"topic"`
	Template string          `yaml:"template"`
	Channel  string          `yaml:"channel"`
	Events   map[string]bool `yaml:"events"`
}

type LegacyResult struct {
	Config Config
	Proof  LegacyImportProof
}

func ReadLegacy(path string, now time.Time) (LegacyResult, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || now.IsZero() {
		return LegacyResult{}, errors.New("invalid legacy notification source")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return LegacyResult{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 ||
		info.Size() < 1 || info.Size() > maximumLegacyConfigBytes {
		return LegacyResult{}, errors.New("legacy notification source must be a private regular file")
	}
	payload, err := os.ReadFile(path)
	if err != nil || len(payload) < 1 || len(payload) > maximumLegacyConfigBytes {
		return LegacyResult{}, errors.New("invalid legacy notification source")
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	if decoder.Decode(&document) != nil {
		return LegacyResult{}, errors.New("invalid legacy notification YAML")
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return LegacyResult{}, errors.New("legacy notification source has trailing YAML")
	}
	root := document.Content
	if len(root) != 1 || root[0].Kind != yaml.MappingNode {
		return LegacyResult{}, errors.New("legacy notification YAML must be a mapping")
	}
	settings := yamlMappingValue(root[0], "settings")
	if settings == nil || settings.Kind != yaml.MappingNode {
		return LegacyResult{}, errors.New("legacy notification settings are absent")
	}
	config := DefaultConfig()
	if timezone := yamlMappingValue(settings, "timezone"); timezone != nil {
		if timezone.Kind != yaml.ScalarNode || timezone.Decode(&config.Timezone) != nil {
			return LegacyResult{}, errors.New("invalid legacy notification timezone")
		}
	}
	warnings := []string{}
	if node := yamlMappingValue(settings, "webhook"); node != nil {
		var legacy legacyWebhook
		if decodeStrictYAMLNode(node, &legacy) != nil {
			return LegacyResult{}, errors.New("invalid legacy webhook config")
		}
		config.Webhook.Enabled, config.Webhook.Format = legacy.Enabled, fallbackString(legacy.Format, "generic")
		config.Webhook.Method, config.Webhook.BodyMode = strings.ToUpper(fallbackString(legacy.Method, "POST")), fallbackString(legacy.BodyMode, "json")
		config.Webhook.URL, config.Webhook.PayloadTemplate = strings.TrimSpace(legacy.URL), legacy.PayloadTemplate
		config.Webhook.Events, warnings = legacySubscriptions(legacy.Events, warnings)
		if value := strings.TrimSpace(legacy.HeadersJSON); value != "" {
			if json.Unmarshal([]byte(value), &config.Webhook.Headers) != nil {
				return LegacyResult{}, errors.New("invalid legacy webhook headers")
			}
		}
		if config.Webhook.Format == "universal_push" {
			config.Webhook.Format, config.Webhook.BodyMode = "custom", "json"
			template, _ := json.Marshal(map[string]string{
				"source": fallbackString(legacy.Source, "otptool"), "title": "{{title}}", "content": "{{content}}",
			})
			config.Webhook.PayloadTemplate = string(template)
			if token := strings.TrimSpace(legacy.Token); token != "" {
				if config.Webhook.Headers == nil {
					config.Webhook.Headers = map[string]string{}
				}
				config.Webhook.Headers["X-App-Token"] = token
			}
			warnings = append(warnings, "legacy_universal_push_converted")
		}
		if legacy.VerifyTLS != nil && !*legacy.VerifyTLS {
			if config.Webhook.Enabled {
				config.Webhook.Enabled = false
			}
			warnings = append(warnings, "legacy_insecure_webhook_disabled")
		}
	}
	if node := yamlMappingValue(settings, "telegram"); node != nil {
		var legacy legacyTelegram
		if decodeStrictYAMLNode(node, &legacy) != nil {
			return LegacyResult{}, errors.New("invalid legacy Telegram config")
		}
		config.Telegram.Enabled = legacy.Enabled
		config.Telegram.BotToken, config.Telegram.ChatID = strings.TrimSpace(legacy.BotToken), strings.TrimSpace(legacy.ChatID)
		config.Telegram.ProxyMode, config.Telegram.ProxyURL = fallbackString(legacy.ProxyMode, "direct"), strings.TrimSpace(legacy.ProxyURL)
		config.Telegram.ProxyCountry = strings.ToLower(strings.TrimSpace(legacy.ProxyCountry))
		config.Telegram.Events, warnings = legacySubscriptions(legacy.Events, warnings)
		if legacy.Commands.Kind != 0 {
			warnings = append(warnings, "legacy_telegram_commands_ignored")
		}
	}
	if node := yamlMappingValue(settings, "pushplus"); node != nil {
		var legacy legacyPushPlus
		if decodeStrictYAMLNode(node, &legacy) != nil {
			return LegacyResult{}, errors.New("invalid legacy PushPlus config")
		}
		config.PushPlus.Enabled = legacy.Enabled
		config.PushPlus.Token, config.PushPlus.Topic = strings.TrimSpace(legacy.Token), strings.TrimSpace(legacy.Topic)
		config.PushPlus.Template, config.PushPlus.Channel = fallbackString(legacy.Template, "html"), fallbackString(legacy.Channel, "wechat")
		config.PushPlus.Events, warnings = legacySubscriptions(legacy.Events, warnings)
	}
	digest := sha256.Sum256(payload)
	proof := LegacyImportProof{SourceSHA256: hex.EncodeToString(digest[:]), ImportedAt: now.UTC(), Warnings: compactWarnings(warnings)}
	config.Imported = &proof
	if err := config.Validate(); err != nil {
		return LegacyResult{}, err
	}
	return LegacyResult{Config: config, Proof: proof}, nil
}

func (store *Store) ImportLegacy(result LegacyResult) (Config, bool, error) {
	if result.Config.Validate() != nil || !hexSHA256.MatchString(result.Proof.SourceSHA256) || result.Proof.ImportedAt.IsZero() {
		return Config{}, false, errors.New("invalid legacy notification import")
	}
	var config Config
	imported := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		current, err := configFromTx(tx)
		if err != nil {
			return err
		}
		if current.Imported != nil {
			if current.Imported.SourceSHA256 != result.Proof.SourceSHA256 {
				return ErrConflict
			}
			config = current
			return nil
		}
		if tx.Bucket(bucketMeta).Get(keySeeded) != nil {
			return ErrConflict
		}
		for _, bucket := range [][]byte{bucketEvents, bucketReceipts, bucketDeliveries, bucketOperations, bucketHostState} {
			if key, _ := tx.Bucket(bucket).Cursor().First(); key != nil {
				return ErrConflict
			}
		}
		if !sameConfig(current, DefaultConfig()) {
			return ErrConflict
		}
		config = cloneConfig(result.Config)
		config.SchemaVersion, config.Revision, config.Imported = SchemaVersion, current.Revision+1, &result.Proof
		if err := config.Validate(); err != nil {
			return err
		}
		if err := putJSON(tx.Bucket(bucketConfig), keyConfig, config); err != nil {
			return err
		}
		imported = true
		return nil
	})
	return cloneConfig(config), imported, err
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func decodeStrictYAMLNode(node *yaml.Node, target any) error {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	if err := encoder.Encode(node); err != nil {
		return err
	}
	_ = encoder.Close()
	decoder := yaml.NewDecoder(&buffer)
	decoder.KnownFields(true)
	return decoder.Decode(target)
}

func legacySubscriptions(input map[string]bool, warnings []string) (Subscriptions, []string) {
	result := defaultSubscriptions()
	for key, enabled := range input {
		switch key {
		case EventIncomingSMS:
			result.IncomingSMS = enabled
		case EventIncomingCall:
			result.IncomingCall = enabled
		case EventHostAlert:
			result.HostAlert = enabled
		case EventActivationReminder:
			result.ActivationReminder = enabled
		case "number_changed":
			warnings = append(warnings, "legacy_number_changed_unsupported")
		case "line_unrecoverable":
			warnings = append(warnings, "legacy_line_unrecoverable_unsupported")
		default:
			warnings = append(warnings, "legacy_notification_event_ignored")
		}
	}
	return result, warnings
}

func compactWarnings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if machineCode(value) && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
