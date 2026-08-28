// Package providerconfig defines the provider-neutral launch contract shared by
// Core deployment tooling and the isolated VoWiFi provider executable.
package providerconfig

import (
	"errors"
	"net"
	"net/netip"
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
		IMSI string `json:"imsi"`
		MCC  string `json:"mcc"`
		MNC  string `json:"mnc"`
		IMEI string `json:"imei"`
		SMSC string `json:"smsc"`
	} `json:"sim"`
	Network struct {
		EPDGAddress    string   `json:"epdg_address"`
		PCSCF          []string `json:"pcscf"`
		IKETimeoutMS   int      `json:"ike_timeout_ms"`
		CloseTimeoutMS int      `json:"close_timeout_ms"`
		MTU            int      `json:"mtu"`
	} `json:"network"`
	IMS struct {
		IMPI             string `json:"impi"`
		IMPU             string `json:"impu"`
		Domain           string `json:"domain"`
		AKAAppPreference string `json:"aka_app_preference"`
		Network          string `json:"network"`
		Server           string `json:"server"`
		TimeoutMS        int    `json:"timeout_ms"`
		Expires          int    `json:"expires"`
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
	return nil
}
