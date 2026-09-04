// Package egressconfig owns the durable desired configuration for country exits.
// Runtime node selection, probe results, routes, and recovery state do not belong here.
package egressconfig

import (
	"errors"
	"sort"
	"strings"
)

const SchemaVersion = 2

type Profile struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	URL            string `json:"url,omitempty"`
	RefreshMinutes int    `json:"refresh_minutes,omitempty"`
	Value          string `json:"value,omitempty"`
	Server         string `json:"server,omitempty"`
	Port           int    `json:"port,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	OutboundTag    string `json:"outbound_tag,omitempty"`
	SIMICCID       string `json:"sim_iccid,omitempty"`
	// RuntimeMode preserves the source type after an executor resolves a
	// durable profile into a temporary loopback transport. It is never
	// serialized into desired state or exposed as a credential-bearing field.
	RuntimeMode string `json:"-"`
}

type Exit struct {
	Enabled    bool     `json:"enabled"`
	Mode       string   `json:"mode,omitempty"`
	ProfileID  string   `json:"profile_id,omitempty"`
	Keywords   []string `json:"keywords,omitempty"`
	PinnedNode string   `json:"pinned_node,omitempty"`
	PinMode    string   `json:"pin_mode,omitempty"`
}

type Config struct {
	SchemaVersion         int                `json:"schema_version"`
	Enabled               bool               `json:"enabled"`
	MissingPolicy         string             `json:"missing_policy,omitempty"`
	SubscriptionURL       string             `json:"subscription_url,omitempty"`
	RefreshMinutes        int                `json:"refresh_minutes,omitempty"`
	ExistingSingboxConfig string             `json:"existing_singbox_config,omitempty"`
	Profiles              map[string]Profile `json:"profiles"`
	Exits                 map[string]Exit    `json:"exits"`
}

type Snapshot struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      uint64 `json:"revision"`
	Config        Config `json:"config"`
}

func (config *Config) normalizeAndValidate() error {
	if config == nil {
		return errors.New("country exit configuration is nil")
	}
	if config.SchemaVersion == 0 {
		config.SchemaVersion = SchemaVersion
	}
	if config.SchemaVersion != SchemaVersion {
		return errors.New("country exit configuration schema is unsupported")
	}
	config.MissingPolicy = strings.ToLower(strings.TrimSpace(config.MissingPolicy))
	if config.MissingPolicy == "" {
		config.MissingPolicy = "error"
	}
	if config.MissingPolicy != "error" {
		return errors.New("country exit missing policy is unsupported")
	}
	config.SubscriptionURL = strings.TrimSpace(config.SubscriptionURL)
	config.ExistingSingboxConfig = strings.TrimSpace(config.ExistingSingboxConfig)
	if len(config.SubscriptionURL) > 8192 || containsControl(config.SubscriptionURL) ||
		len(config.ExistingSingboxConfig) > 4096 || containsControl(config.ExistingSingboxConfig) {
		return errors.New("country exit compatibility setting is invalid")
	}
	if config.RefreshMinutes == 0 {
		config.RefreshMinutes = 30
	}
	if config.RefreshMinutes < 1 || config.RefreshMinutes > 10080 {
		return errors.New("country exit refresh interval must be between 1 and 10080 minutes")
	}
	if config.Profiles == nil {
		config.Profiles = map[string]Profile{}
	}
	if config.Exits == nil {
		config.Exits = map[string]Exit{}
	}
	if len(config.Profiles) > 256 || len(config.Exits) > 249 {
		return errors.New("country exit configuration is too large")
	}
	for id, profile := range config.Profiles {
		if !validIdentifier(id, 80) {
			return errors.New("country exit profile ID is invalid")
		}
		profile.Name = strings.TrimSpace(profile.Name)
		profile.Type = strings.ToLower(strings.TrimSpace(profile.Type))
		profile.URL = strings.TrimSpace(profile.URL)
		profile.Value = strings.TrimSpace(profile.Value)
		profile.Server = strings.TrimSpace(profile.Server)
		profile.Username = strings.TrimSpace(profile.Username)
		profile.OutboundTag = strings.TrimSpace(profile.OutboundTag)
		profile.SIMICCID = digitsOnly(profile.SIMICCID)
		if profile.Name == "" || len(profile.Name) > 256 || containsControl(profile.Name) {
			return errors.New("country exit profile name is invalid")
		}
		for _, value := range []string{profile.URL, profile.Value, profile.Server, profile.Username,
			profile.Password, profile.OutboundTag} {
			if len(value) > 32768 || containsControl(value) {
				return errors.New("country exit profile contains an invalid value")
			}
		}
		switch profile.Type {
		case "subscription":
			if profile.URL == "" {
				return errors.New("subscription profile URL is required")
			}
			if profile.RefreshMinutes == 0 {
				profile.RefreshMinutes = config.RefreshMinutes
			}
			if profile.RefreshMinutes < 1 || profile.RefreshMinutes > 10080 {
				return errors.New("subscription refresh interval is invalid")
			}
		case "node":
			if profile.Value == "" {
				return errors.New("node profile value is required")
			}
		case "socks5":
			if profile.Server == "" || profile.Port < 1 || profile.Port > 65535 ||
				strings.ContainsAny(profile.Server, " /\t\r\n") {
				return errors.New("SOCKS5 profile server or port is invalid")
			}
		case "existing":
			if profile.OutboundTag == "" {
				return errors.New("existing profile outbound tag is required")
			}
		case "cellular_sim":
			if !digitsBetween(profile.SIMICCID, 18, 22) {
				return errors.New("cellular data profile ICCID is invalid")
			}
		default:
			return errors.New("country exit profile type is unsupported")
		}
		config.Profiles[id] = profile
	}
	normalizedExits := make(map[string]Exit, len(config.Exits))
	for rawCountry, exit := range config.Exits {
		country, ok := normalizeCountry(rawCountry)
		if !ok {
			return errors.New("country exit country is invalid")
		}
		if _, duplicate := normalizedExits[country]; duplicate {
			return errors.New("country exit configuration contains duplicate countries")
		}
		exit.Mode = strings.ToLower(strings.TrimSpace(exit.Mode))
		exit.ProfileID = strings.TrimSpace(exit.ProfileID)
		exit.PinnedNode = strings.TrimSpace(exit.PinnedNode)
		exit.PinMode = strings.ToLower(strings.TrimSpace(exit.PinMode))
		if exit.PinnedNode == "" {
			// Older settings could retain the presentation-only value "auto" after
			// the pinned node was cleared. It carries no desired-state meaning.
			exit.PinMode = ""
		}
		if exit.Mode != "" && exit.Mode != "direct" {
			return errors.New("country exit mode is invalid")
		}
		if exit.Mode != "direct" {
			if exit.ProfileID == "" {
				return errors.New("country exit profile is required")
			}
			if _, exists := config.Profiles[exit.ProfileID]; !exists {
				return errors.New("country exit references an unknown profile")
			}
		}
		if len(exit.PinnedNode) > 512 || containsControl(exit.PinnedNode) ||
			(exit.PinMode != "" && exit.PinMode != "lock" && exit.PinMode != "prefer") {
			return errors.New("country exit node selection is invalid")
		}
		if exit.PinnedNode != "" && exit.PinMode == "" {
			exit.PinMode = "lock"
		}
		exit.Keywords = cleanKeywords(exit.Keywords)
		if len(exit.Keywords) > 32 {
			return errors.New("country exit has too many keywords")
		}
		for _, keyword := range exit.Keywords {
			if len(keyword) > 128 || containsControl(keyword) {
				return errors.New("country exit keyword is invalid")
			}
		}
		normalizedExits[country] = exit
	}
	config.Exits = normalizedExits
	return nil
}

func cloneConfig(config Config) Config {
	copy := config
	copy.Profiles = make(map[string]Profile, len(config.Profiles))
	for id, profile := range config.Profiles {
		copy.Profiles[id] = profile
	}
	copy.Exits = make(map[string]Exit, len(config.Exits))
	for country, exit := range config.Exits {
		exit.Keywords = append([]string(nil), exit.Keywords...)
		copy.Exits[country] = exit
	}
	return copy
}

func normalizeCountry(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 2 || value[0] < 'a' || value[0] > 'z' || value[1] < 'a' || value[1] > 'z' {
		return "", false
	}
	return value, true
}

func validIdentifier(value string, maximum int) bool {
	if len(value) < 1 || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
			strings.ContainsRune("-_.", char) {
			continue
		}
		return false
	}
	return true
}

func containsControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func digitsOnly(value string) string {
	var result strings.Builder
	for _, char := range strings.TrimSpace(value) {
		if char >= '0' && char <= '9' {
			result.WriteRune(char)
		} else if char != ' ' && char != '-' && char != '\t' {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func digitsBetween(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func cleanKeywords(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
