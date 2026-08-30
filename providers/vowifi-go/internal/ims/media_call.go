// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

var ErrMediaNegotiation = errors.New("IMS media negotiation failed")

const mediaCleanupTimeout = 5 * time.Second

const amrNBBandwidthEfficientFMTP = "octet-align=0;crc=0;robust-sorting=0;interleaving=0"

// MediaCallConfig selects the existing browser PCM contract and the local SWu
// endpoints to advertise in SDP. Local endpoints must belong to the userspace
// stack; :0 reserves a free port before INVITE is sent.
type MediaCallConfig struct {
	LocalRTP  string
	LocalRTCP string
	Codec     media.Codec
	BufferMS  int
	// Lifetime is independent of the bounded INVITE context. Cancelling it
	// stops media; when nil, End is the sole owner of media shutdown.
	Lifetime context.Context
}

// MediaCall couples one upstream SIP dialog to one userspace RTP/RTCP bridge.
// It deliberately does not own registration recovery or browser heartbeat
// policy. End always closes media, even when the peer does not accept BYE.
type MediaCall struct {
	agent  *voicehost.IMSOutboundAgent
	bridge *media.Bridge
	dialog voicehost.DialogInfo

	endMu     sync.Mutex
	ended     bool
	endResult voicehost.DialogInfoResult
}

// StartMediaCall reserves userspace media ports, sends the SDP offer through
// the already-registered IMS agent, then atomically applies the SDP answer.
func StartMediaCall(
	ctx context.Context,
	agent *voicehost.IMSOutboundAgent,
	stack *usernet.Stack,
	config MediaCallConfig,
	request voicehost.OutboundCallRequest,
) (*MediaCall, voicehost.OutboundCallResult, error) {
	if agent == nil {
		return nil, voicehost.OutboundCallResult{}, ErrVoiceNotReady
	}
	if stack == nil {
		return nil, voicehost.OutboundCallResult{}, fmt.Errorf("%w: userspace stack is nil", ErrMediaNegotiation)
	}
	if strings.TrimSpace(request.CallID) == "" {
		return nil, voicehost.OutboundCallResult{}, fmt.Errorf("%w: Call-ID is empty", ErrMediaNegotiation)
	}
	if config.LocalRTP == "" || config.LocalRTCP == "" {
		return nil, voicehost.OutboundCallResult{}, fmt.Errorf("%w: local RTP and RTCP endpoints are required", ErrMediaNegotiation)
	}
	lifetime := config.Lifetime
	if lifetime == nil {
		lifetime = context.Background()
	}
	bridge, err := media.Open(lifetime, stack, media.Config{
		LocalRTP: config.LocalRTP, LocalRTCP: config.LocalRTCP,
		Codec: config.Codec, BufferMS: config.BufferMS,
	})
	if err != nil {
		return nil, voicehost.OutboundCallResult{}, err
	}
	closeBridge := true
	defer func() {
		if closeBridge {
			closeMedia(bridge)
		}
	}()

	localSDP, err := mediaOffer(bridge, config.Codec)
	if err != nil {
		return nil, voicehost.OutboundCallResult{}, err
	}
	request.RawSDP = voicehost.BuildSDPAnswerWithOptions(localSDP, voicehost.SDPAnswerOptions{
		Codecs:  []voicehost.SDPCodec{sdpCodec(config.Codec)},
		PTimeMS: 20, MaxPTimeMS: 20,
	})
	request.RemoteSDP = localSDP
	result, err := agent.StartOutboundCall(ctx, request)
	if err != nil || !result.Accepted {
		return nil, result, err
	}

	remoteRTP, remoteRTCP, err := acceptedMediaEndpoints(result, config.Codec)
	if err == nil {
		err = bridge.SetRemote(remoteRTP, remoteRTCP)
	}
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), mediaCleanupTimeout)
		_, byeErr := agent.EndVoiceCallWithResult(cleanupCtx, voicehost.DialogInfo{
			DeviceID: request.DeviceID, CallID: request.CallID,
		})
		cancel()
		if byeErr != nil {
			return nil, result, errors.Join(err, fmt.Errorf("end accepted call after media failure: %w", byeErr))
		}
		return nil, result, err
	}

	closeBridge = false
	return &MediaCall{
		agent: agent, bridge: bridge,
		dialog: voicehost.DialogInfo{DeviceID: request.DeviceID, CallID: request.CallID},
	}, result, nil
}

// End serializes BYE attempts and then stops RTP/RTCP regardless of the BYE
// outcome. A successful result is idempotent; a failure remains retryable by
// the caller because closing local media is not evidence of remote hangup.
func (call *MediaCall) End(ctx context.Context) (voicehost.DialogInfoResult, error) {
	if call == nil {
		return voicehost.DialogInfoResult{}, nil
	}
	call.endMu.Lock()
	defer call.endMu.Unlock()
	if call.ended {
		return call.endResult, nil
	}
	result, err := call.agent.EndVoiceCallWithResult(ctx, call.dialog)
	if closeErr := closeMedia(call.bridge); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err == nil && result.Accepted {
		call.ended = true
		call.endResult = result
	}
	return result, err
}

func (call *MediaCall) SendDTMF(ctx context.Context, signal string, durationMS int) (string, error) {
	if call == nil || call.agent == nil {
		return "", errors.New("IMS call is unavailable")
	}
	request := voicehost.DialogRTPDTMFRequest{
		DeviceID: call.dialog.DeviceID, CallID: call.dialog.CallID,
		Direction: voicehost.RTPDTMFClientToIMS, Signal: signal, DurationMS: durationMS,
	}
	rtp, err := call.agent.SendDialogRTPDTMF(ctx, request)
	if err == nil {
		if !rtp.Accepted {
			return "", fmt.Errorf("IMS rejected RTP DTMF with %d %s", rtp.StatusCode, rtp.Reason)
		}
		return voicehost.DialogDTMFRouteRTP, nil
	}
	if !errors.Is(err, voicehost.ErrRTPRelayConfig) {
		return "", err
	}
	info, infoErr := call.agent.SendDialogDTMF(ctx, voicehost.DialogDTMFRequest{
		DeviceID: call.dialog.DeviceID, CallID: call.dialog.CallID,
		Signal: signal, DurationMS: durationMS,
	})
	if infoErr != nil {
		return "", infoErr
	}
	if !info.Accepted {
		return "", fmt.Errorf("IMS rejected DTMF with SIP %d %s", info.StatusCode, info.Reason)
	}
	return voicehost.DialogDTMFRouteInfo, nil
}

func (call *MediaCall) WritePCM(frame []byte, capturedAt time.Time) (bool, error) {
	if call == nil {
		return false, media.ErrClosed
	}
	return call.bridge.WritePCM(frame, capturedAt)
}

func (call *MediaCall) PCM() <-chan media.PCMFrame {
	if call == nil {
		return (*media.Bridge)(nil).PCM()
	}
	return call.bridge.PCM()
}

func (call *MediaCall) Errors() <-chan error {
	if call == nil {
		return (*media.Bridge)(nil).Errors()
	}
	return call.bridge.Errors()
}

func (call *MediaCall) Stats() media.Stats {
	if call == nil {
		return media.Stats{}
	}
	return call.bridge.Stats()
}

func mediaOffer(bridge *media.Bridge, codec media.Codec) (voicehost.SDPInfo, error) {
	rtpAddress, rtcpAddress, err := bridge.LocalEndpoints()
	if err != nil {
		return voicehost.SDPInfo{}, err
	}
	rtp, err := literalAddrPort(rtpAddress)
	if err != nil {
		return voicehost.SDPInfo{}, fmt.Errorf("%w: local RTP: %v", ErrMediaNegotiation, err)
	}
	rtcp, err := literalAddrPort(rtcpAddress)
	if err != nil {
		return voicehost.SDPInfo{}, fmt.Errorf("%w: local RTCP: %v", ErrMediaNegotiation, err)
	}
	if rtp.Addr() != rtcp.Addr() {
		return voicehost.SDPInfo{}, fmt.Errorf("%w: RTP and RTCP must use the same local IP", ErrMediaNegotiation)
	}
	return voicehost.SDPInfo{
		ConnectionIP: rtp.Addr().String(), MediaPort: int(rtp.Port()),
		RTCPIP: rtcp.Addr().String(), RTCPPort: int(rtcp.Port()),
		Payloads: []int{int(codec.PayloadType())}, Direction: "sendrecv",
		PTimeMS: 20, MaxPTimeMS: 20,
	}, nil
}

func acceptedMediaEndpoints(result voicehost.OutboundCallResult, codec media.Codec) (string, string, error) {
	description, err := voicehost.ParseSDPMediaDescription(result.RawSDP)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid SDP answer: %v", ErrMediaNegotiation, err)
	}
	if description.RTCPMux {
		return "", "", fmt.Errorf("%w: RTCP mux is not supported", ErrMediaNegotiation)
	}
	info := description.Info
	if info.MediaPort <= 0 || strings.TrimSpace(info.ConnectionIP) == "" {
		return "", "", fmt.Errorf("%w: remote audio stream was rejected", ErrMediaNegotiation)
	}
	if direction := strings.ToLower(strings.TrimSpace(info.Direction)); direction != "" && direction != "sendrecv" {
		return "", "", fmt.Errorf("%w: remote audio direction %q is not bidirectional", ErrMediaNegotiation, direction)
	}
	want := sdpCodec(codec)
	found := false
	for _, candidate := range description.Codecs {
		if candidate.Payload == want.Payload && strings.EqualFold(candidate.EncodingName, want.EncodingName) &&
			(candidate.ClockRate == 0 || candidate.ClockRate == want.ClockRate) {
			if codec == media.CodecAMR {
				compatibility := voicehost.ClassifySDPAMRFMTPCompatibility(candidate.FMTP, amrNBBandwidthEfficientFMTP)
				if !compatibility.Compatible {
					continue
				}
			}
			found = true
			break
		}
	}
	if !found {
		return "", "", fmt.Errorf("%w: answer did not accept %s payload %d", ErrMediaNegotiation, want.EncodingName, want.Payload)
	}
	if info.PTimeMS > 0 && info.PTimeMS != 20 {
		return "", "", fmt.Errorf("%w: unsupported answer ptime %d ms", ErrMediaNegotiation, info.PTimeMS)
	}
	if info.MaxPTimeMS > 0 && info.MaxPTimeMS < 20 {
		return "", "", fmt.Errorf("%w: answer maxptime %d ms is below 20 ms", ErrMediaNegotiation, info.MaxPTimeMS)
	}
	remoteIP, err := netip.ParseAddr(strings.TrimSpace(info.ConnectionIP))
	if err != nil || remoteIP.IsUnspecified() || remoteIP.IsMulticast() {
		return "", "", fmt.Errorf("%w: remote RTP address must be a literal unicast IP", ErrMediaNegotiation)
	}
	rtcpIP := remoteIP
	if strings.TrimSpace(info.RTCPIP) != "" {
		rtcpIP, err = netip.ParseAddr(strings.TrimSpace(info.RTCPIP))
		if err != nil || rtcpIP.IsUnspecified() || rtcpIP.IsMulticast() {
			return "", "", fmt.Errorf("%w: remote RTCP address must be a literal unicast IP", ErrMediaNegotiation)
		}
	}
	rtcpPort := info.RTCPPort
	if rtcpPort <= 0 {
		rtcpPort = info.MediaPort + 1
	}
	if info.MediaPort > 65535 || rtcpPort <= 0 || rtcpPort > 65535 {
		return "", "", fmt.Errorf("%w: remote media port is out of range", ErrMediaNegotiation)
	}
	return netip.AddrPortFrom(remoteIP, uint16(info.MediaPort)).String(),
		netip.AddrPortFrom(rtcpIP, uint16(rtcpPort)).String(), nil
}

func sdpCodec(codec media.Codec) voicehost.SDPCodec {
	switch codec {
	case media.CodecPCMA:
		return voicehost.NewSDPPCMACodec()
	case media.CodecAMR:
		// RFC 4867 defaults to bandwidth-efficient mode when octet-align is
		// absent. This matches the proven legacy Asterisk wire contract.
		return voicehost.NewSDPAMRCodec(int(media.CodecAMR.PayloadType()), "")
	default:
		return voicehost.NewSDPPCMUCodec()
	}
}

func literalAddrPort(address net.Addr) (netip.AddrPort, error) {
	if address == nil {
		return netip.AddrPort{}, errors.New("address is empty")
	}
	endpoint, err := netip.ParseAddrPort(address.String())
	if err != nil || !endpoint.Addr().IsValid() || endpoint.Addr().IsUnspecified() || endpoint.Port() == 0 {
		return netip.AddrPort{}, errors.New("address must be a literal IP and non-zero port")
	}
	return endpoint, nil
}

func closeMedia(bridge *media.Bridge) error {
	ctx, cancel := context.WithTimeout(context.Background(), mediaCleanupTimeout)
	defer cancel()
	if err := bridge.Close(ctx); err != nil {
		return fmt.Errorf("close IMS media: %w", err)
	}
	return nil
}
