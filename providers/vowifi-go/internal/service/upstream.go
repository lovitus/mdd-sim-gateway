// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"crypto/sha256"
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
	IDRMode                   string
	PDNFamily                 string
	ProxyURL                  string
	IMPI, IMPU, IMSDomain     string
	UserAgent                 string
	AccessNetworkInfo         string
	VisitedNetworkID          string
	AccessType                string
	UserEqualsPhone           bool
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
	selectedFamily  string
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
	config.IDRMode = strings.ToLower(strings.TrimSpace(config.IDRMode))
	if config.IDRMode == "" {
		config.IDRMode = "apn"
	}
	config.PDNFamily = strings.ToLower(strings.TrimSpace(config.PDNFamily))
	if config.PDNFamily == "" {
		config.PDNFamily = "auto"
	}
	config.ProxyURL = strings.TrimSpace(config.ProxyURL)
	config.BrokerURL = strings.TrimSpace(config.BrokerURL)
	config.SIPNetwork = strings.TrimSpace(config.SIPNetwork)
	config.SIPServer = strings.TrimSpace(config.SIPServer)
	config.UserAgent = strings.TrimSpace(config.UserAgent)
	config.AccessNetworkInfo = strings.TrimSpace(config.AccessNetworkInfo)
	config.VisitedNetworkID = strings.TrimSpace(config.VisitedNetworkID)
	config.AccessType = strings.TrimSpace(config.AccessType)
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
		config.SIPNetwork = "tcp"
	}
	if config.LineID == "" || config.DeviceID == "" || config.Profile.IMSI == "" ||
		config.BrokerURL == "" || len(config.BrokerToken) < 32 || config.IKETimeout > time.Minute ||
		config.SIPTimeout > time.Minute || config.CloseTimeout > 30*time.Second {
		return nil, errors.New("invalid upstream VoWiFi runtime configuration")
	}
	if config.SIPNetwork != "udp" && config.SIPNetwork != "tcp" {
		return nil, errors.New("IMS SIP network must be udp or tcp")
	}
	if config.IDRMode != "apn" && config.IDRMode != "fqdn" {
		return nil, errors.New("VoWiFi IDr mode must be apn or fqdn")
	}
	if config.IDRMode == "fqdn" && (!exactDigits(config.Profile.MCC, 3, 3) || !exactDigits(config.Profile.MNC, 2, 3)) {
		return nil, errors.New("VoWiFi FQDN IDr requires an exact MCC and MNC")
	}
	if config.PDNFamily != "auto" && config.PDNFamily != "v4" && config.PDNFamily != "v6" && config.PDNFamily != "dual" {
		return nil, errors.New("VoWiFi PDN family must be auto, v4, v6, or dual")
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
	factory.mu.Unlock()
	effectiveFamily, candidateOffset, pinnedFamily := factory.beginNetworkAttempt()
	established := false
	defer func() { factory.completeNetworkAttempt(effectiveFamily, pinnedFamily, established) }()
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
	outer, err := outerudp.New(outerudp.Config{ProxyURL: config.ProxyURL, CandidateOffset: candidateOffset})
	if err != nil {
		return nil, &StageError{Layer: "tunnel", Code: "outer_transport_invalid", Err: err}
	}
	configuration, selectors := swuPDNConfiguration(effectiveFamily)
	responderID := responderIdentity(config.IMSAPN, config.IDRMode, config.Profile.MCC, config.Profile.MNC)
	swuProvider, err := provider.NewUpstream(upstreamswu.IKEPacketTunnelManagerConfig{
		SIM: simProvider, Timeout: config.IKETimeout,
		ResponderID:           ikev2.Identity{Type: ikev2.IDFQDN, Data: []byte(responderID)},
		InitialContact:        true,
		EAPOnlyAuth:           true,
		ForceUDPEncapsulation: config.ProxyURL != "",
		SA:                    swuIKEProposalForDH(ikev2.DHGroup2048BitMODP),
		InitRunner:            runSWUIKEInit,
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
		UserAgent: config.UserAgent, AccessNetworkInfo: config.AccessNetworkInfo,
		VisitedNetworkID: config.VisitedNetworkID, AccessType: config.AccessType,
		UserEqualsPhone:                config.UserEqualsPhone,
		DisableDerivedVisitedNetworkID: true,
		ContactUser:                    contactUser(config.LineID, config.DeviceID, config.TraceID), SMSEnabled: true,
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
		localTag := contactUser(config.LineID, config.DeviceID, config.TraceID) + "-inbound"
		if err := inbound.ConfigureVoice(stack, localIP.String(), registration.Binding.ContactURI, localTag, config.UserAgent,
			registration.Profile, registration.Binding, registration.VoiceTransport); err != nil {
			if registration.Close != nil {
				_ = closeBounded(config.CloseTimeout, registration.Close)
			}
			_ = closeBounded(config.CloseTimeout, stack.Close)
			return nil, &StageError{Layer: "voice", Code: "inbound_voice_unavailable", Err: err}
		}
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
		pdnFamily: effectiveFamily, responderID: responderID,
		messaging: messagingService, tracker: tracker, inbound: inbound,
	}
	messagingService.SetSMSTransport(registration.SMSTransport)
	go runtime.observeStack()
	established = true
	return runtime, nil
}

// swuIKEProposalForDH mirrors the already deployed strongSwan compatibility
// set without carrier-specific identifiers. Modern group 14 is always tried
// first; group 2 is passed only by runSWUIKEInit's bounded fallback.
func swuIKEProposalForDH(group uint16) ikev2.SecurityAssociation {
	proposal := func(number uint8, bits, prf, integrity uint16) ikev2.Proposal {
		return ikev2.Proposal{
			Number:     number,
			ProtocolID: ikev2.ProtocolIKE,
			Transforms: []ikev2.Transform{
				{Type: ikev2.TransformENCR, ID: ikev2.ENCR_AES_CBC, Attributes: []ikev2.TransformAttribute{ikev2.KeyLengthAttribute(bits)}},
				{Type: ikev2.TransformPRF, ID: prf},
				{Type: ikev2.TransformINTEG, ID: integrity},
				{Type: ikev2.TransformDHRGroup, ID: group},
			},
		}
	}
	return ikev2.SecurityAssociation{Proposals: []ikev2.Proposal{
		proposal(1, 256, ikev2.PRF_HMAC_SHA2_256, ikev2.INTEG_HMAC_SHA2_256_128),
		proposal(2, 128, ikev2.PRF_HMAC_SHA2_256, ikev2.INTEG_HMAC_SHA2_256_128),
		proposal(3, 256, ikev2.PRF_HMAC_SHA1, ikev2.INTEG_HMAC_SHA1_96),
		proposal(4, 128, ikev2.PRF_HMAC_SHA1, ikev2.INTEG_HMAC_SHA1_96),
	}}
}

func runSWUIKEInit(ctx context.Context, config ikev2.InitConfig) (ikev2.InitResult, error) {
	return runSWUIKEInitWith(ctx, config, ikev2.RunIKE_SA_INIT)
}

func runSWUIKEInitWith(ctx context.Context, config ikev2.InitConfig, run upstreamswu.IKEInitRunner) (ikev2.InitResult, error) {
	result, err := run(ctx, config)
	if err == nil || !shouldFallbackLegacyIKE(err) {
		return result, err
	}
	legacy := config
	legacy.SA = swuIKEProposalForDH(ikev2.DHGroup1024BitMODP)
	// A retry is a new IKE_SA_INIT exchange, not a replay with the rejected
	// SPI, nonce or key-exchange material.
	legacy.InitiatorSPI = 0
	legacy.NonceI = nil
	legacy.X25519PrivateKey = nil
	result, fallbackErr := run(ctx, legacy)
	if fallbackErr != nil {
		return ikev2.InitResult{}, errors.Join(err, fmt.Errorf("legacy MODP-1024 IKE fallback failed: %w", fallbackErr))
	}
	return result, nil
}

func shouldFallbackLegacyIKE(err error) bool {
	if errors.Is(err, ikev2.ErrNotifyNoProposalChosen) {
		return true
	}
	group, ok, parseErr := ikev2.InvalidKEPayloadAlternativeGroupFromError(err)
	return parseErr == nil && ok && group == ikev2.DHGroup1024BitMODP
}

func contactUser(lineID, deviceID, traceID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(lineID) + "\x00" + strings.TrimSpace(deviceID) + "\x00" + strings.TrimSpace(traceID)))
	// The legacy IMS runtime used a UUID contact user. This value only routes
	// inbound SIP requests within one registration; it is not an equipment ID.
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func responderIdentity(apn, mode, mcc, mnc string) string {
	apn = strings.ToLower(strings.TrimSpace(apn))
	if mode != "fqdn" {
		return apn
	}
	return fmt.Sprintf("%s.apn.epc.mnc%s.mcc%s.pub.3gppnetwork.org", apn,
		leftPadDigits(mnc, 3), leftPadDigits(mcc, 3))
}

func pdnFamilyOrder(mode, mcc, mnc string) []string {
	if mode != "auto" {
		return []string{mode}
	}
	preferred := map[string]string{
		"302-220": "v6",
		"234-15":  "dual",
		"234-30":  "v6",
		"234-33":  "v6",
	}
	keys := []string{mcc + "-" + mnc, leftPadDigits(mcc, 3) + "-" + leftPadDigits(mnc, 3),
		mcc + "-" + strings.TrimLeft(mnc, "0")}
	order := make([]string, 0, 3)
	for _, key := range keys {
		if value := preferred[key]; value != "" {
			order = append(order, value)
			break
		}
	}
	for _, value := range []string{"v6", "dual", "v4"} {
		found := false
		for _, existing := range order {
			found = found || existing == value
		}
		if !found {
			order = append(order, value)
		}
	}
	return order
}

func pdnAttempt(mode, mcc, mnc string, attempt uint64) (string, uint64) {
	order := pdnFamilyOrder(mode, mcc, mnc)
	return order[attempt%uint64(len(order))], attempt / uint64(len(order))
}

func (factory *UpstreamFactory) beginNetworkAttempt() (family string, candidateOffset uint64, pinned bool) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	attempt := factory.endpointAttempt
	factory.endpointAttempt++
	if factory.selectedFamily != "" {
		return factory.selectedFamily, attempt, true
	}
	family, candidateOffset = pdnAttempt(factory.config.PDNFamily, factory.config.Profile.MCC, factory.config.Profile.MNC, attempt)
	return family, candidateOffset, false
}

func (factory *UpstreamFactory) completeNetworkAttempt(family string, pinned, success bool) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if success {
		factory.selectedFamily = family
	} else if pinned && factory.selectedFamily == family {
		factory.selectedFamily = ""
	}
}

func leftPadDigits(value string, width int) string {
	value = strings.TrimSpace(value)
	for len(value) < width {
		value = "0" + value
	}
	return value
}

func exactDigits(value string, minimum, maximum int) bool {
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
	stack                *usernet.Stack
	registration         runtimehost.IMSRegistrationResult
	registrationMu       sync.RWMutex
	registrationRevision uint64
	recoveryMu           sync.Mutex
	closeTimeout         time.Duration
	deviceID             string
	imsi                 string
	localIP              string
	pdnFamily            string
	responderID          string
	messaging            *messaging.Service
	tracker              *messageTracker
	inbound              *inboundMessaging

	faultMu sync.Mutex
	fault   error
}

func (runtime *upstreamRuntime) NetworkSelection() (string, string) {
	return runtime.pdnFamily, runtime.responderID
}

func (runtime *upstreamRuntime) SendMessage(ctx context.Context, request vowifiipc.SendMessageRequest) error {
	registration, _ := runtime.registrationSnapshot()
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
	call, result, err := runtime.startMediaCallWithRecovery(ctx, func(registration runtimehost.IMSRegistrationResult) (VoiceCall, voicehost.OutboundCallResult, error) {
		agent, agentErr := ims.NewOutboundAgent(registration)
		if agentErr != nil {
			return nil, voicehost.OutboundCallResult{}, &StageError{Layer: "voice", Code: "voice_transport_unavailable", Err: agentErr}
		}
		return ims.StartMediaCall(ctx, agent, runtime.stack, ims.MediaCallConfig{
			LocalRTP: net.JoinHostPort(runtime.localIP, "0"), LocalRTCP: net.JoinHostPort(runtime.localIP, "0"),
			Codec: media.CodecAMR, BufferMS: request.MediaBufferMS,
		}, voicehost.OutboundCallRequest{
			DeviceID: runtime.deviceID, CallID: request.CallID, Callee: request.Callee,
		})
	})
	if err != nil {
		var stage *StageError
		if errors.As(err, &stage) {
			return nil, err
		}
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

type mediaCallAttempt func(runtimehost.IMSRegistrationResult) (VoiceCall, voicehost.OutboundCallResult, error)

func (runtime *upstreamRuntime) startMediaCallWithRecovery(ctx context.Context, attempt mediaCallAttempt) (VoiceCall, voicehost.OutboundCallResult, error) {
	registration, revision := runtime.registrationSnapshot()
	call, result, err := attempt(registration)
	if ctx.Err() != nil || !result.RegistrationRecoveryNeeded {
		return call, result, err
	}

	recovered, recoveryApplied, recoveryErr := runtime.recoverCallRegistration(ctx, revision, result.RetryAfter)
	if recoveryErr != nil {
		return nil, result, errors.Join(err, fmt.Errorf("IMS registration recovery: %w", recoveryErr))
	}
	// Match the upstream runtime boundary: a transport failure retries the same
	// Call-ID once after recovery. A carrier response is returned to the caller
	// unchanged, even if it also prompted a registration refresh.
	if err == nil || !recoveryApplied {
		return call, result, err
	}
	return attempt(recovered)
}

func (runtime *upstreamRuntime) recoverCallRegistration(ctx context.Context, failedRevision uint64, retryAfter time.Duration) (runtimehost.IMSRegistrationResult, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.recoveryMu.Lock()
	defer runtime.recoveryMu.Unlock()

	registration, revision := runtime.registrationSnapshot()
	if revision != failedRevision {
		return registration, true, nil
	}
	if registration.Recover == nil {
		return registration, false, nil
	}
	if retryAfter > 0 {
		timer := time.NewTimer(retryAfter)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return registration, false, ctx.Err()
		case <-timer.C:
		}
	}
	recovered, err := registration.Recover(ctx)
	if err != nil {
		return registration, false, err
	}
	if !recovered.Registered {
		return recovered, false, fmt.Errorf("registration did not recover: %d %s", recovered.StatusCode, strings.TrimSpace(recovered.Reason))
	}
	runtime.registrationMu.Lock()
	if runtime.registrationRevision == revision {
		runtime.registration = recovered
		runtime.registrationRevision++
	}
	runtime.registrationMu.Unlock()
	if runtime.messaging != nil && recovered.SMSTransport != nil {
		runtime.messaging.SetSMSTransport(recovered.SMSTransport)
	}
	return recovered, true, nil
}

func (runtime *upstreamRuntime) registrationSnapshot() (runtimehost.IMSRegistrationResult, uint64) {
	runtime.registrationMu.RLock()
	registration := runtime.registration
	revision := runtime.registrationRevision
	runtime.registrationMu.RUnlock()
	if registration.Snapshot != nil {
		registration = registration.Snapshot()
	}
	return registration, revision
}

func (runtime *upstreamRuntime) PendingIncomingCall() (vowifiipc.PendingIncomingCall, bool) {
	if runtime == nil || runtime.inbound == nil {
		return vowifiipc.PendingIncomingCall{}, false
	}
	pending, found := runtime.inbound.PendingCall()
	if !found {
		return vowifiipc.PendingIncomingCall{}, false
	}
	return vowifiipc.PendingIncomingCall{
		CallID: pending.CallID, Caller: pending.Caller, Callee: pending.Callee, ReceivedAt: pending.ReceivedAt,
	}, true
}

func (runtime *upstreamRuntime) SetIncomingCallAvailability(available func() bool) {
	if runtime != nil && runtime.inbound != nil {
		runtime.inbound.SetCallAvailability(available)
	}
}

func (runtime *upstreamRuntime) AnswerIncomingCall(ctx context.Context, callID string, bufferMS int) (VoiceCall, error) {
	if runtime == nil || runtime.inbound == nil {
		return nil, ims.ErrIncomingCallNotFound
	}
	return runtime.inbound.AnswerCall(ctx, callID, bufferMS)
}

func (runtime *upstreamRuntime) RejectIncomingCall(callID string) error {
	if runtime == nil || runtime.inbound == nil {
		return ims.ErrIncomingCallNotFound
	}
	return runtime.inbound.RejectCall(callID)
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
	registration, _ := runtime.registrationSnapshot()
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
	var inboundErr error
	if runtime.inbound != nil {
		inboundErr = runtime.inbound.Close(ctx)
	}
	var registrationErr error
	registration, _ := runtime.registrationSnapshot()
	if registration.Close != nil {
		registrationErr = registration.Close(ctx)
	}
	stackErr := runtime.stack.Close(ctx)
	return classifyUpstreamClose(registrationErr, inboundErr, stackErr, runtime.stack.Released())
}

func classifyUpstreamClose(registrationErr, inboundErr, stackErr error, stackReleased bool) error {
	failure := errors.Join(registrationErr, inboundErr, stackErr)
	if failure != nil && stackReleased {
		// The userspace stack owns the local packet session, sockets and packet
		// protector. Once it confirms shutdown, SIP, IKE and remote transport
		// errors cannot be repaired by retaining this runtime.
		return &localResourcesReleasedError{cause: failure}
	}
	return failure
}

type localResourcesReleasedError struct{ cause error }

func (failure *localResourcesReleasedError) Error() string      { return failure.cause.Error() }
func (failure *localResourcesReleasedError) Unwrap() error      { return failure.cause }
func (*localResourcesReleasedError) LocalRuntimeReleased() bool { return true }

var _ Factory = (*UpstreamFactory)(nil)
var _ Runtime = (*upstreamRuntime)(nil)
var _ MessagingRuntime = (*upstreamRuntime)(nil)
