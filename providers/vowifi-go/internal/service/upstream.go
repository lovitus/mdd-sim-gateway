// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	upstreamswu "github.com/boa-z/vowifi-go/engine/swu"
	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/boa-z/vowifi-go/runtimehost/messaging"
	"github.com/boa-z/vowifi-go/runtimehost/simauth"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/agentaka"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/ims"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/provider"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

type UpstreamConfig struct {
	LineID, DeviceID, TraceID string
	Profile                   identity.Profile
	EPDGAddress               string
	PCSCF                     []string
	IMPI, IMPU, IMSDomain     string
	AKAAppPreference          string
	Agent                     agentaka.Config
	BrokerURL, BrokerToken    string
	IKETimeout, SIPTimeout    time.Duration
	CloseTimeout              time.Duration
	MTU                       int
	SIPNetwork, SIPServer     string
	SIPExpires                int
}

type UpstreamFactory struct{ config UpstreamConfig }

func NewUpstreamFactory(config UpstreamConfig) (*UpstreamFactory, error) {
	config.LineID = strings.TrimSpace(config.LineID)
	config.DeviceID = strings.TrimSpace(config.DeviceID)
	config.TraceID = strings.TrimSpace(config.TraceID)
	config.EPDGAddress = strings.TrimSpace(config.EPDGAddress)
	config.BrokerURL = strings.TrimSpace(config.BrokerURL)
	config.SIPNetwork = strings.TrimSpace(config.SIPNetwork)
	config.SIPServer = strings.TrimSpace(config.SIPServer)
	if config.IKETimeout <= 0 {
		config.IKETimeout = 30 * time.Second
	}
	if config.SIPTimeout <= 0 {
		config.SIPTimeout = 15 * time.Second
	}
	if config.CloseTimeout <= 0 {
		config.CloseTimeout = 5 * time.Second
	}
	if config.SIPNetwork == "" {
		config.SIPNetwork = "udp"
	}
	if config.LineID == "" || config.DeviceID == "" || config.Profile.IMSI == "" ||
		config.BrokerURL == "" || len(config.BrokerToken) < 32 || config.IKETimeout > time.Minute ||
		config.SIPTimeout > time.Minute || config.CloseTimeout > 30*time.Second {
		return nil, errors.New("invalid upstream VoWiFi runtime configuration")
	}
	if config.SIPNetwork != "udp" && config.SIPNetwork != "tcp" {
		return nil, errors.New("IMS SIP network must be udp or tcp")
	}
	return &UpstreamFactory{config: config}, nil
}

func (factory *UpstreamFactory) Start(ctx context.Context) (Runtime, error) {
	if factory == nil {
		return nil, errors.New("nil upstream VoWiFi factory")
	}
	config := factory.config
	broker := agentlink.BrokerClient{URL: config.BrokerURL, Token: config.BrokerToken}
	authenticator, err := agentaka.New(broker, config.Agent)
	if err != nil {
		return nil, &StageError{Layer: "sim", Code: "agent_aka_invalid", Err: err}
	}
	simProvider := simauth.NewAKAHostProvider(authenticator, nil)
	prepared, err := prepareIdentity(config)
	if err != nil {
		return nil, &StageError{Layer: "sim", Code: "identity_invalid", Err: err}
	}
	swuProvider, err := provider.NewUpstream(upstreamswu.IKEPacketTunnelManagerConfig{
		SIM: simProvider, Timeout: config.IKETimeout,
	})
	if err != nil {
		return nil, &StageError{Layer: "tunnel", Code: "swu_provider_invalid", Err: err}
	}
	packetSession, err := swuProvider.Open(ctx, provider.Config{
		LineID: config.LineID, DeviceID: config.DeviceID, TraceID: config.TraceID,
		IMSI: prepared.Profile.IMSI, MCC: prepared.Profile.MCC, MNC: prepared.Profile.MNC,
		EPDGAddress: prepared.EPDGAddr, CloseTimeout: config.CloseTimeout,
	})
	if err != nil {
		return nil, &StageError{Layer: "tunnel", Code: "swu_open_failed", Err: err}
	}
	info := packetSession.Info()
	localIP, err := netip.ParseAddr(strings.TrimSpace(info.LocalInnerIP))
	if err != nil || !localIP.IsGlobalUnicast() {
		_ = closeBounded(config.CloseTimeout, packetSession.Close)
		return nil, &StageError{Layer: "tunnel", Code: "inner_address_invalid", Err: fmt.Errorf("invalid local inner IP %q", info.LocalInnerIP)}
	}
	dns, err := parseDNS(info.DNSServers)
	if err != nil {
		_ = closeBounded(config.CloseTimeout, packetSession.Close)
		return nil, &StageError{Layer: "tunnel", Code: "inner_dns_invalid", Err: err}
	}
	stack, err := usernet.Open(ctx, packetSession, usernet.Config{
		Addresses: []netip.Addr{localIP}, DNS: dns, MTU: config.MTU, CloseTimeout: config.CloseTimeout,
	})
	if err != nil {
		_ = closeBounded(config.CloseTimeout, packetSession.Close)
		return nil, &StageError{Layer: "tunnel", Code: "userspace_stack_failed", Err: err}
	}
	registrar, err := ims.NewRegistrar(stack, runtimehost.WireIMSRegistrar{
		Network: config.SIPNetwork, ServerAddr: config.SIPServer, ContactHost: localIP.String(),
		Timeout: config.SIPTimeout, Expires: config.SIPExpires,
	})
	if err != nil {
		_ = closeBounded(config.CloseTimeout, stack.Close)
		return nil, &StageError{Layer: "ims", Code: "ims_config_invalid", Err: err}
	}
	tunnel := upstreamswu.TunnelResult{
		Ready: true, Mode: upstreamswu.DataplaneModeUserspace,
		EPDGAddress: info.EPDGAddress, LocalInnerIP: info.LocalInnerIP, RemoteInnerIP: info.RemoteInnerIP,
		DNSServers: append([]string(nil), info.DNSServers...), IKEEstablished: true, IPsecEstablished: true,
	}
	registration, err := registrar.RegisterIMS(ctx, runtimehost.IMSRegistrationConfig{
		DeviceID: config.DeviceID, TraceID: config.TraceID,
		Profile: prepared.Profile, Prepared: &prepared, SIM: &knownSIM{provider: simProvider, imsi: prepared.Profile.IMSI},
		NetworkMode: "vowifi", Dataplane: runtimehost.DataplanePolicy{Mode: upstreamswu.DataplaneModeUserspace},
		Tunnel: tunnel,
	})
	if err != nil || !registration.Registered {
		closeErr := closeBounded(config.CloseTimeout, stack.Close)
		if err == nil {
			err = fmt.Errorf("IMS registration rejected: %d %s", registration.StatusCode, strings.TrimSpace(registration.Reason))
		}
		return nil, &StageError{Layer: "ims", Code: "ims_register_failed", Err: errors.Join(err, closeErr)}
	}
	runtime := &upstreamRuntime{
		stack: stack, registration: registration, closeTimeout: config.CloseTimeout,
		deviceID: config.DeviceID, imsi: prepared.Profile.IMSI, localIP: localIP.String(),
	}
	go runtime.observeStack()
	return runtime, nil
}

type knownSIM struct {
	provider *simauth.AKAHostProvider
	imsi     string
}

func (sim *knownSIM) GetIMSI() (string, error) { return sim.imsi, nil }
func (sim *knownSIM) CalculateAKA(rand16, autn16 []byte) (simauth.AKAResult, error) {
	return sim.provider.CalculateAKA(rand16, autn16)
}
func (sim *knownSIM) CalculateAKAWithPreference(rand16, autn16 []byte, preference string) (simauth.AKAResult, error) {
	return sim.provider.CalculateAKAWithPreference(rand16, autn16, preference)
}
func (*knownSIM) Close() error { return nil }

func prepareIdentity(config UpstreamConfig) (identity.PreparedSession, error) {
	prepared, err := identity.PrepareStart(identity.PrepareStartInput{
		DeviceID: config.DeviceID, Profile: config.Profile, RuntimeEPDGOverride: config.EPDGAddress,
	})
	if err != nil {
		return identity.PreparedSession{}, err
	}
	if len(config.PCSCF) > 0 {
		prepared.PCSCFFQDNs = cleanStrings(config.PCSCF)
		if len(prepared.PCSCFFQDNs) == 0 {
			return identity.PreparedSession{}, errors.New("explicit P-CSCF list is empty")
		}
	}
	if config.IMPI != "" || config.IMPU != "" || config.IMSDomain != "" {
		if strings.TrimSpace(config.IMPI) == "" || strings.TrimSpace(config.IMPU) == "" || strings.TrimSpace(config.IMSDomain) == "" {
			return identity.PreparedSession{}, errors.New("explicit IMS identity must include IMPI, IMPU, and domain")
		}
		prepared.IMSIdentity = identity.IMSIdentityResolution{
			RequestedSource: identity.IMSIdentitySourceProfile, ActualSource: identity.IMSIdentitySourceProfile,
			AKAAppPreference: strings.TrimSpace(config.AKAAppPreference), Applied: true,
			IMPI: strings.TrimSpace(config.IMPI), IMPU: strings.TrimSpace(config.IMPU), Domain: strings.TrimSpace(config.IMSDomain),
		}
		if prepared.IMSIdentity.AKAAppPreference == "" {
			prepared.IMSIdentity.AKAAppPreference = identity.AKAAppPreferenceUSIM
		}
	}
	if preference := strings.TrimSpace(config.AKAAppPreference); preference != "" {
		switch preference {
		case identity.AKAAppPreferenceUSIM, identity.AKAAppPreferenceAuto,
			identity.AKAAppPreferenceISIM, identity.AKAAppPreferenceISIMStrict:
			prepared.IMSIdentity.AKAAppPreference = preference
		default:
			return identity.PreparedSession{}, fmt.Errorf("unsupported AKA application preference %q", preference)
		}
	}
	if strings.TrimSpace(prepared.EPDGAddr) == "" {
		return identity.PreparedSession{}, errors.New("ePDG address is empty")
	}
	return prepared, nil
}

func parseDNS(values []string) ([]netip.Addr, error) {
	out := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		address, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || address.IsUnspecified() {
			return nil, fmt.Errorf("invalid inner DNS address %q", value)
		}
		out = append(out, address)
	}
	return out, nil
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func closeBounded(timeout time.Duration, close func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return close(ctx)
}

type upstreamRuntime struct {
	stack        *usernet.Stack
	registration runtimehost.IMSRegistrationResult
	closeTimeout time.Duration
	deviceID     string
	imsi         string
	localIP      string

	faultMu sync.Mutex
	fault   error
}

func (runtime *upstreamRuntime) SendMessage(ctx context.Context, request vowifiipc.SendMessageRequest) error {
	registration := runtime.registration
	if registration.Snapshot != nil {
		registration = registration.Snapshot()
	}
	if !registration.Registered || registration.SMSTransport == nil {
		return &vowifiipc.OperationError{
			Kind: vowifiipc.ErrorNotReady, Code: "messaging_transport_unavailable", Layer: "messaging",
		}
	}
	service := messaging.NewService(runtime.deviceID, runtime.imsi, nil, nil)
	service.SetSMSTransport(registration.SMSTransport)
	_, err := service.SendSMSWithOptions(ctx, request.Recipient, request.Body, messaging.SendOptions{})
	return err
}

func (runtime *upstreamRuntime) StartMediaCall(ctx context.Context, request vowifiipc.StartCallRequest) (VoiceCall, error) {
	registration := runtime.registration
	if registration.Snapshot != nil {
		registration = registration.Snapshot()
	}
	agent, err := ims.NewOutboundAgent(registration)
	if err != nil {
		return nil, &StageError{Layer: "voice", Code: "voice_transport_unavailable", Err: err}
	}
	call, result, err := ims.StartMediaCall(ctx, agent, runtime.stack, ims.MediaCallConfig{
		LocalRTP: net.JoinHostPort(runtime.localIP, "0"), LocalRTCP: net.JoinHostPort(runtime.localIP, "0"),
		Codec: media.CodecPCMU, BufferMS: request.MediaBufferMS,
	}, voicehost.OutboundCallRequest{
		DeviceID: runtime.deviceID, CallID: request.CallID, Callee: request.Callee,
	})
	if err != nil {
		return nil, &StageError{Layer: "voice", Code: "call_start_failed", Err: err}
	}
	if !result.Accepted || call == nil {
		failure := &vowifiipc.OperationError{
			Kind: vowifiipc.ErrorRejected, Code: "call_rejected", Layer: "voice",
			Detail: strings.TrimSpace(result.Reason), RetryAfter: result.RetryAfter,
		}
		if result.RetryAfter > 0 {
			failure.RetryAfterMS = result.RetryAfter.Milliseconds()
		}
		return nil, failure
	}
	return call, nil
}

func (runtime *upstreamRuntime) observeStack() {
	if err, ok := <-runtime.stack.Errors(); ok && err != nil {
		runtime.faultMu.Lock()
		runtime.fault = err
		runtime.faultMu.Unlock()
	}
}

func (runtime *upstreamRuntime) Layers() Layers {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	runtime.faultMu.Lock()
	fault := runtime.fault
	runtime.faultMu.Unlock()
	if fault != nil {
		blocked := vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "userspace_stack_failed"}
		return Layers{Tunnel: vowifiipc.LayerStatus{Condition: vowifiipc.LayerDegraded, Code: "userspace_stack_failed"}, IMS: blocked, Voice: blocked, Messaging: blocked}
	}
	registration := runtime.registration
	if registration.Snapshot != nil {
		registration = registration.Snapshot()
	}
	if !registration.Registered {
		blocked := vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "ims_not_registered"}
		return Layers{Tunnel: ready, IMS: blocked, Voice: blocked, Messaging: blocked}
	}
	voice := ready
	if _, err := ims.NewOutboundAgent(registration); err != nil {
		voice = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "voice_transport_unavailable"}
	}
	messaging := ready
	if registration.SMSTransport == nil {
		messaging = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "messaging_transport_unavailable"}
	}
	return Layers{Tunnel: ready, IMS: ready, Voice: voice, Messaging: messaging}
}

func (runtime *upstreamRuntime) Close(ctx context.Context) error {
	if ctx == nil {
		return closeBounded(runtime.closeTimeout, runtime.Close)
	}
	var registrationErr error
	if runtime.registration.Close != nil {
		registrationErr = runtime.registration.Close(ctx)
	}
	stackErr := runtime.stack.Close(ctx)
	return errors.Join(registrationErr, stackErr)
}

var _ Factory = (*UpstreamFactory)(nil)
var _ Runtime = (*upstreamRuntime)(nil)
var _ MessagingRuntime = (*upstreamRuntime)(nil)
