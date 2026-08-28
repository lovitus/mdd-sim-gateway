// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	upstreamswu "github.com/boa-z/vowifi-go/engine/swu"
	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/carrier"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/boa-z/vowifi-go/runtimehost/messaging"
	"github.com/boa-z/vowifi-go/runtimehost/simauth"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/agentaka"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/ims"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/outerudp"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/provider"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

type UpstreamConfig struct {
	LineID, DeviceID, TraceID string
	Profile                   identity.Profile
	EPDGAddress               string
	PCSCF                     []string
	IMSAPN                    string
	PDNFamily                 string
	ProxyURL                  string
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

type UpstreamFactory struct {
	config          UpstreamConfig
	mu              sync.Mutex
	sink            messageSinkBinding
	endpointAttempt uint64
}

type messageSinkBinding struct {
	sink       MessageSink
	providerID string
	generation string
}

func (factory *UpstreamFactory) SetMessageSink(sink MessageSink, providerID, generation string) error {
	if factory == nil || sink == nil || strings.TrimSpace(providerID) == "" || strings.TrimSpace(generation) == "" {
		return errors.New("invalid provider message sink binding")
	}
	factory.mu.Lock()
	factory.sink = messageSinkBinding{sink: sink, providerID: strings.TrimSpace(providerID), generation: strings.TrimSpace(generation)}
	factory.mu.Unlock()
	return nil
}

func NewUpstreamFactory(config UpstreamConfig) (*UpstreamFactory, error) {
	config.LineID = strings.TrimSpace(config.LineID)
	config.DeviceID = strings.TrimSpace(config.DeviceID)
	config.TraceID = strings.TrimSpace(config.TraceID)
	config.EPDGAddress = strings.TrimSpace(config.EPDGAddress)
	config.IMSAPN = strings.ToLower(strings.TrimSpace(config.IMSAPN))
	if config.IMSAPN == "" {
		config.IMSAPN = carrier.DefaultIMSAPN()
	}
	config.PDNFamily = strings.ToLower(strings.TrimSpace(config.PDNFamily))
	if config.PDNFamily == "" {
		config.PDNFamily = "v6"
	}
	config.ProxyURL = strings.TrimSpace(config.ProxyURL)
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
	if config.PDNFamily != "v4" && config.PDNFamily != "v6" && config.PDNFamily != "dual" {
		return nil, errors.New("VoWiFi PDN family must be v4, v6, or dual")
	}
	if err := validateProxyURL(config.ProxyURL); err != nil {
		return nil, err
	}
	return &UpstreamFactory{config: config}, nil
}

func (factory *UpstreamFactory) Start(ctx context.Context) (Runtime, error) {
	if factory == nil {
		return nil, errors.New("nil upstream VoWiFi factory")
	}
	config := factory.config
	factory.mu.Lock()
	sinkBinding := factory.sink
	endpointAttempt := factory.endpointAttempt
	factory.endpointAttempt++
	factory.mu.Unlock()
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
	outer, err := outerudp.New(outerudp.Config{ProxyURL: config.ProxyURL, CandidateOffset: endpointAttempt})
	if err != nil {
		return nil, &StageError{Layer: "tunnel", Code: "outer_transport_invalid", Err: err}
	}
	configuration, selectors := swuPDNConfiguration(config.PDNFamily)
	swuProvider, err := provider.NewUpstream(upstreamswu.IKEPacketTunnelManagerConfig{
		SIM: simProvider, Timeout: config.IKETimeout,
		ResponderID:           ikev2.Identity{Type: ikev2.IDFQDN, Data: []byte(config.IMSAPN)},
		InitialContact:        true,
		EAPOnlyAuth:           true,
		ForceUDPEncapsulation: config.ProxyURL != "",
		SA:                    ikev2.DefaultIKEProposalForDH(ikev2.DHGroup2048BitMODP),
		Configuration:         configuration, TSi: selectors, TSr: selectors,
		IKETransportFactory: func(_ upstreamswu.TunnelConfig, transport upstreamswu.IKETransportConfig) (ikev2.InitTransport, error) {
			if err := outer.Bind(transport.RemoteAddr, transport.Timeout); err != nil {
				return nil, err
			}
			return outer, nil
		},
		ESPTransportFactory: func(_ upstreamswu.TunnelConfig, transport upstreamswu.ESPTransportConfig) (upstreamswu.ESPPacketTransport, error) {
			if err := outer.Bind(transport.RemoteAddr, transport.Timeout); err != nil {
				return nil, err
			}
			return outer, nil
		},
	})
	if err != nil {
		_ = outer.Close(context.Background())
		return nil, &StageError{Layer: "tunnel", Code: "swu_provider_invalid", Err: err}
	}
	packetSession, err := swuProvider.Open(ctx, provider.Config{
		LineID: config.LineID, DeviceID: config.DeviceID, TraceID: config.TraceID,
		IMSI: prepared.Profile.IMSI, MCC: prepared.Profile.MCC, MNC: prepared.Profile.MNC,
		EPDGAddress: prepared.EPDGAddr, CloseTimeout: config.CloseTimeout,
	})
	if err != nil {
		if endpoint := outer.SelectedRemote(); endpoint != "" {
			err = fmt.Errorf("ePDG endpoint %s: %w", endpoint, err)
		}
		_ = closeBounded(config.CloseTimeout, outer.Close)
		return nil, &StageError{Layer: "tunnel", Code: "swu_open_failed", Err: err}
	}
	info := packetSession.Info()
	if len(config.PCSCF) == 0 && len(info.PCSCFServers) > 0 {
		prepared.PCSCFFQDNs = cleanStrings(info.PCSCFServers)
	}
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
	identity := func(eventID string, kind providermessages.Kind) providermessages.Event {
		return providermessages.Event{
			SchemaVersion: providermessages.SchemaVersion, EventID: sinkBinding.generation + ":" + eventID,
			LineID: config.LineID, ProviderID: sinkBinding.providerID, ProcessGeneration: sinkBinding.generation,
			Kind: kind, ObservedAt: time.Now().UTC(),
		}
	}
	tracker := newMessageTracker(sinkBinding.sink, identity)
	messagingService := messaging.NewService(config.DeviceID, prepared.Profile.IMSI, tracker, nil)
	var inbound *inboundMessaging
	if sinkBinding.sink != nil {
		inbound, err = newInboundMessaging(messagingService, tracker, sinkBinding.sink)
		if err != nil {
			_ = closeBounded(config.CloseTimeout, stack.Close)
			return nil, &StageError{Layer: "messaging", Code: "inbound_handler_failed", Err: err}
		}
	}
	registrar, err := ims.NewRegistrar(stack, runtimehost.WireIMSRegistrar{
		Network: config.SIPNetwork, ServerAddr: config.SIPServer, ContactHost: localIP.String(),
		ContactPort: 5060, Timeout: config.SIPTimeout, Expires: config.SIPExpires, IncomingHandler: inbound,
	})
	if err != nil {
		if inbound != nil {
			_ = closeBounded(config.CloseTimeout, inbound.Close)
		}
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
		stats := packetSession.PacketStats()
		if inbound != nil {
			_ = closeBounded(config.CloseTimeout, inbound.Close)
		}
		closeErr := closeBounded(config.CloseTimeout, stack.Close)
		if err == nil {
			err = fmt.Errorf("IMS registration rejected: %d %s", registration.StatusCode, strings.TrimSpace(registration.Reason))
		}
		err = imsRegisterDiagnostic(err, info.PCSCFServers, stats)
		return nil, &StageError{Layer: "ims", Code: "ims_register_failed", Err: errors.Join(err, closeErr)}
	}
	if inbound != nil {
		flow, ok := registration.VoiceTransport.(inboundSIPFlow)
		if !ok {
			if registration.Close != nil {
				_ = closeBounded(config.CloseTimeout, registration.Close)
			}
			_ = closeBounded(config.CloseTimeout, stack.Close)
			return nil, &StageError{Layer: "messaging", Code: "inbound_flow_unavailable", Err: errors.New("registered SIP transport cannot serve inbound requests")}
		}
		if err := inbound.Start(flow); err != nil {
			if registration.Close != nil {
				_ = closeBounded(config.CloseTimeout, registration.Close)
			}
			_ = closeBounded(config.CloseTimeout, stack.Close)
			return nil, &StageError{Layer: "messaging", Code: "inbound_flow_failed", Err: err}
		}
	}
	runtime := &upstreamRuntime{
		stack: stack, registration: registration, closeTimeout: config.CloseTimeout,
		deviceID: config.DeviceID, imsi: prepared.Profile.IMSI, localIP: localIP.String(),
		messaging: messagingService, tracker: tracker, inbound: inbound,
	}
	messagingService.SetSMSTransport(registration.SMSTransport)
	go runtime.observeStack()
	return runtime, nil
}

func swuPDNConfiguration(family string) (ikev2.Configuration, ikev2.TrafficSelectors) {
	switch family {
	case "v4":
		return ikev2.SWuIPv4ConfigurationRequest(), ikev2.IPv4AnyTrafficSelectors()
	case "dual":
		return ikev2.SWuDualStackConfigurationRequest(), ikev2.DualStackAnyTrafficSelectors()
	default:
		return ikev2.SWuIPv6ConfigurationRequest(), ikev2.IPv6AnyTrafficSelectors()
	}
}

func imsRegisterDiagnostic(err error, pcscf []string, stats upstreamswu.PacketTunnelStats) error {
	targets := strings.Join(cleanStrings(pcscf), ",")
	if targets == "" {
		targets = "<none>"
	}
	return fmt.Errorf(
		"P-CSCF candidates %s; SWu packets tx_inner=%d tx_esp=%d rx_esp=%d rx_inner=%d tx_errors=%d rx_errors=%d invalid_drops=%d replay_drops=%d: %w",
		targets,
		stats.OutboundInnerPackets, stats.OutboundESPPackets,
		stats.InboundESPPackets, stats.InboundInnerPackets,
		stats.OutboundErrors, stats.InboundErrors,
		stats.InvalidDrops, stats.ReplayDrops,
		err,
	)
}

func validateProxyURL(value string) error {
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
		password, found := parsed.User.Password()
		if parsed.User.Username() == "" || !found || password == "" {
			return errors.New("VoWiFi proxy credentials are incomplete")
		}
	}
	return nil
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
	messaging    *messaging.Service
	tracker      *messageTracker
	inbound      *inboundMessaging

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
	messagingService := runtime.messaging
	if messagingService == nil {
		// Narrow unit fixtures constructed before the inbound service existed
		// still exercise the registered upstream transport directly. Production
		// runtimes always install the long-lived service in Start.
		messagingService = messaging.NewService(runtime.deviceID, runtime.imsi, nil, nil)
	}
	messagingService.SetSMSTransport(registration.SMSTransport)
	_, err := messagingService.SendSMSWithOptions(ctx, request.Recipient, request.Body, messaging.SendOptions{MessageID: request.MessageID})
	if err == nil && runtime.tracker != nil {
		if persistErr := runtime.tracker.takeFailure(request.MessageID); persistErr != nil {
			return &vowifiipc.OperationError{
				Kind: vowifiipc.ErrorFailed, Code: "message_status_persist_failed", Layer: "messaging",
				Detail: "message was submitted but its delivery identity could not be persisted",
			}
		}
	}
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
	} else if runtime.inbound == nil {
		messaging = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "messaging_event_sink_unavailable"}
	} else if runtime.inbound.Fault() != nil {
		messaging = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "inbound_messaging_failed"}
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
	var inboundErr error
	if runtime.inbound != nil {
		inboundErr = runtime.inbound.Close(ctx)
	}
	stackErr := runtime.stack.Close(ctx)
	return errors.Join(registrationErr, inboundErr, stackErr)
}

var _ Factory = (*UpstreamFactory)(nil)
var _ Runtime = (*upstreamRuntime)(nil)
var _ MessagingRuntime = (*upstreamRuntime)(nil)
