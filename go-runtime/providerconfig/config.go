// Package providerconfig defines the provider-neutral launch contract shared by
// Core deployment tooling and the isolated VoWiFi provider executable.
package providerconfig

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
)

type Config struct {
	LineID     string `json:"line_id"`
	ProviderID string `json:"provider_id"`
	DeviceID   string `json:"device_id"`
	TraceID    string `json:"trace_id"`
	IPC        struct {
		Listen             string `json:"listen"`
		Token              string `json:"token"`
		StatePath          string `json:"state_path"`
		OperationTimeoutMS int    `json:"operation_timeout_ms"`
		ShutdownTimeoutMS  int    `json:"shutdown_timeout_ms"`
		MediaCapacity      int    `json:"media_capacity"`
		CallGuardTimeoutMS int    `json:"call_guard_timeout_ms"`
	} `json:"ipc"`
	Core struct {
		RegistrationURL   string `json:"registration_url"`
		RegistrationToken string `json:"registration_token"`
		RefreshMS         int    `json:"refresh_ms"`
	} `json:"core"`
	Agent struct {
		BrokerURL   string `json:"broker_url"`
		BrokerToken string `json:"broker_token"`
		CardID      string `json:"card_id"`
		TimeoutMS   int    `json:"timeout_ms"`
	} `json:"agent"`
	SIM struct {
		IMSI   string `json:"imsi"`
		MCC    string `json:"mcc"`
		MNC    string `json:"mnc"`
		IMEI   string `json:"imei"`
		IMEISV string `json:"imeisv,omitempty"`
		SMSC   string `json:"smsc"`
	} `json:"sim"`
	Network struct {
		EPDGAddress    string   `json:"epdg_address"`
		PCSCF          []string `json:"pcscf"`
		IMSAPN         string   `json:"ims_apn,omitempty"`
		IDRMode        string   `json:"idr_mode,omitempty"`
		PDNFamily      string   `json:"pdn_family,omitempty"`
		ProxyURL       string   `json:"proxy_url,omitempty"`
		IKETimeoutMS   int      `json:"ike_timeout_ms"`
		CloseTimeoutMS int      `json:"close_timeout_ms"`
		MTU            int      `json:"mtu"`
	} `json:"network"`
	IMS struct {
		IMPI              string `json:"impi"`
		IMPU              string `json:"impu"`
		Domain            string `json:"domain"`
		UserAgent         string `json:"user_agent,omitempty"`
		AccessNetworkInfo string `json:"access_network_info,omitempty"`
		VisitedNetworkID  string `json:"visited_network_id,omitempty"`
		AccessType        string `json:"access_type,omitempty"`
		UserEqualsPhone   bool   `json:"user_equals_phone,omitempty"`
		AKAAppPreference  string `json:"aka_app_preference"`
		Network           string `json:"network"`
		Server            string `json:"server"`
		TimeoutMS         int    `json:"timeout_ms"`
		Expires           int    `json:"expires"`
	} `json:"ims"`
}

func (settings Config) Validate() error {
	if strings.TrimSpace(settings.LineID) == "" || strings.TrimSpace(settings.ProviderID) == "" ||
		strings.TrimSpace(settings.DeviceID) == "" || len(settings.IPC.Token) < 32 ||
		len(settings.Agent.BrokerToken) < 32 || strings.TrimSpace(settings.IPC.StatePath) == "" {
		return errors.New("VoWiFi config is missing runtime identity or token")
	}
	host, port, err := net.SplitHostPort(settings.IPC.Listen)
	if err != nil || port == "" {
		return errors.New("VoWiFi IPC listen address must include a port")
	}
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !address.IsLoopback() {
		return errors.New("VoWiFi IPC must listen on a literal loopback address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("VoWiFi IPC listen port must be between 0 and 65535")
	}
	if !filepath.IsAbs(settings.IPC.StatePath) {
		return errors.New("VoWiFi operation state path must be absolute")
	}
	if err := validateProxyURL(settings.Network.ProxyURL); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(settings.Network.PDNFamily)) {
	case "", "auto", "v4", "v6", "dual":
	default:
		return errors.New("VoWiFi PDN family must be auto, v4, v6, or dual")
	}
	switch strings.ToLower(strings.TrimSpace(settings.Network.IDRMode)) {
	case "", "apn", "fqdn":
	default:
		return errors.New("VoWiFi IDr mode must be apn or fqdn")
	}
	if len(settings.Network.IMSAPN) > 100 || strings.TrimSpace(settings.Network.IMSAPN) != settings.Network.IMSAPN ||
		strings.ContainsAny(settings.Network.IMSAPN, "\r\n\x00\"") {
		return errors.New("VoWiFi IMS APN is invalid")
	}
	if strings.EqualFold(strings.TrimSpace(settings.Network.IDRMode), "fqdn") &&
		(!configDigits(settings.SIM.MCC, 3, 3) || !configDigits(settings.SIM.MNC, 2, 3)) {
		return errors.New("VoWiFi FQDN IDr requires an exact MCC and MNC")
	}
	if settings.SIM.IMEISV != "" && !configDigits(settings.SIM.IMEISV, 16, 16) {
		return errors.New("VoWiFi IMEISV is invalid")
	}
	registrationURL := strings.TrimSpace(settings.Core.RegistrationURL)
	registrationToken := strings.TrimSpace(settings.Core.RegistrationToken)
	if (registrationURL == "") != (registrationToken == "") {
		return errors.New("Core provider registration URL and token must be configured together")
	}
	if registrationURL != "" {
		if settings.Core.RefreshMS != 0 && (settings.Core.RefreshMS < 1000 || settings.Core.RefreshMS > 25_000) {
			return errors.New("Core provider registration refresh must be between 1 and 25 seconds")
		}
		if err := (mediaauth.RegistrationClient{URL: registrationURL, Token: registrationToken}).Validate(); err != nil {
			return err
		}
	}
	for _, value := range []int{
		settings.IPC.OperationTimeoutMS, settings.IPC.ShutdownTimeoutMS, settings.Agent.TimeoutMS,
		settings.Network.IKETimeoutMS, settings.Network.CloseTimeoutMS, settings.IMS.TimeoutMS,
	} {
		if value < 0 || value > 120_000 {
			return errors.New("VoWiFi timeout must be between 0 and 120000 ms")
		}
	}
	if settings.IPC.CallGuardTimeoutMS < 0 || settings.IPC.CallGuardTimeoutMS > 60_000 {
		return errors.New("call guard timeout must be between 0 and 60000 ms")
	}
	for _, value := range []string{settings.IMS.UserAgent, settings.IMS.AccessNetworkInfo, settings.IMS.VisitedNetworkID, settings.IMS.AccessType} {
		if len(value) > 1024 || strings.ContainsAny(value, "\r\n") {
			return errors.New("IMS SIP presentation contains an invalid header value")
		}
	}
	return nil
}

func configDigits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func validateProxyURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "socks5" || parsed.Hostname() == "" || parsed.Port() == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("VoWiFi proxy must be an exact socks5 URL with host and port")
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil || port == 0 {
		return errors.New("VoWiFi proxy port is invalid")
	}
	if parsed.User != nil {
		username := parsed.User.Username()
		password, hasPassword := parsed.User.Password()
		if username == "" || !hasPassword || password == "" {
			return errors.New("VoWiFi proxy credentials must include username and password")
		}
	}
	return nil
}
