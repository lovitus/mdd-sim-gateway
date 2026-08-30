// SPDX-License-Identifier: AGPL-3.0-only

// Package media terminates IMS RTP/RTCP inside the SWu userspace network and
// exposes the existing MDD browser PCM frame contract. It does not own call
// signalling, browser heartbeats, or hangup policy.
package media

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

const (
	SampleRate     = 8000
	FrameSamples   = 160
	PCMFrameBytes  = FrameSamples * 2
	FrameDuration  = 20 * time.Millisecond
	defaultRTCPGap = 5 * time.Second
)

var (
	ErrInvalidConfig = errors.New("invalid IMS media config")
	ErrClosed        = errors.New("IMS media bridge is closed")
)

type Codec string

const (
	CodecPCMU Codec = "PCMU"
	CodecPCMA Codec = "PCMA"
	CodecAMR  Codec = "AMR"
)

func (codec Codec) PayloadType() uint8 {
	switch codec {
	case CodecPCMA:
		return 8
	case CodecAMR:
		return 96
	default:
		return 0
	}
}

type Config struct {
	LocalRTP   string
	LocalRTCP  string
	RemoteRTP  string
	RemoteRTCP string
	Codec      Codec
	BufferMS   int

	SSRC             uint32
	InitialSequence  uint16
	InitialTimestamp uint32
	RTCPInterval     time.Duration
}

type PCMFrame struct {
	Data       []byte
	CapturedAt time.Time
}

type Stats struct {
	BrowserFramesAccepted uint64
	BrowserFramesStale    uint64
	BrowserFramesDropped  uint64
	SilenceFramesSent     uint64
	RTPPacketsSent        uint64
	RTPPacketsReceived    uint64
	RTPPacketsRejected    uint64
	RTPPacketsLost        uint64
	PCMFramesDelivered    uint64
	PCMFramesDropped      uint64
	RTCPPacketsSent       uint64
	RTCPPacketsReceived   uint64
}

type Bridge struct {
	rtpConn  net.PacketConn
	rtcpConn net.PacketConn
	rtpPeer  *net.UDPAddr
	rtcpPeer *net.UDPAddr
	codec    Codec
	frames   frameCodec
	buffer   time.Duration
	ssrc     uint32

	ctx       context.Context
	cancel    context.CancelFunc
	in        chan PCMFrame
	out       chan PCMFrame
	errors    chan error
	done      chan struct{}
	peerReady chan struct{}
	wait      sync.WaitGroup

	sequence  uint16
	timestamp uint32
	rtcpGap   time.Duration

	mu              sync.Mutex
	stats           Stats
	remoteSeen      bool
	remoteSequence  uint16
	remoteCycles    uint32
	remoteSSRC      uint32
	sentOctets      uint32
	previousSilence bool
	peerReadyOnce   sync.Once
	shutdownOnce    sync.Once
	closeOutputOnce sync.Once
}

func Open(ctx context.Context, stack *usernet.Stack, config Config) (*Bridge, error) {
	if stack == nil {
		return nil, fmt.Errorf("%w: userspace stack is nil", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Codec != CodecPCMU && config.Codec != CodecPCMA && config.Codec != CodecAMR {
		return nil, fmt.Errorf("%w: codec must be PCMU, PCMA, or AMR", ErrInvalidConfig)
	}
	if config.BufferMS < 100 || config.BufferMS > 2000 {
		return nil, fmt.Errorf("%w: buffer must be between 100 and 2000 ms", ErrInvalidConfig)
	}
	var (
		rtpPeer, rtcpPeer *net.UDPAddr
		err               error
	)
	if (config.RemoteRTP == "") != (config.RemoteRTCP == "") {
		return nil, fmt.Errorf("%w: remote RTP and RTCP must be set together", ErrInvalidConfig)
	}
	if config.RemoteRTP != "" {
		rtpPeer, err = udpEndpoint(config.RemoteRTP)
		if err != nil {
			return nil, fmt.Errorf("%w: remote RTP: %v", ErrInvalidConfig, err)
		}
		rtcpPeer, err = udpEndpoint(config.RemoteRTCP)
		if err != nil {
			return nil, fmt.Errorf("%w: remote RTCP: %v", ErrInvalidConfig, err)
		}
	}
	if config.RTCPInterval == 0 {
		config.RTCPInterval = defaultRTCPGap
	}
	if config.RTCPInterval < FrameDuration || config.RTCPInterval > 30*time.Second {
		return nil, fmt.Errorf("%w: RTCP interval is outside the bounded range", ErrInvalidConfig)
	}
	if config.SSRC == 0 {
		if err := binary.Read(rand.Reader, binary.BigEndian, &config.SSRC); err != nil {
			return nil, fmt.Errorf("create RTP SSRC: %w", err)
		}
		if config.SSRC == 0 {
			config.SSRC = 1
		}
	}
	frames, err := openFrameCodec(config.Codec)
	if err != nil {
		return nil, err
	}
	rtpConn, err := stack.ListenPacket(ctx, "udp", config.LocalRTP)
	if err != nil {
		frames.Close()
		return nil, fmt.Errorf("listen userspace RTP: %w", err)
	}
	rtcpConn, err := stack.ListenPacket(ctx, "udp", config.LocalRTCP)
	if err != nil {
		_ = rtpConn.Close()
		frames.Close()
		return nil, fmt.Errorf("listen userspace RTCP: %w", err)
	}
	runContext, cancel := context.WithCancel(context.Background())
	capacity := (config.BufferMS + 19) / 20
	bridge := &Bridge{
		rtpConn: rtpConn, rtcpConn: rtcpConn, rtpPeer: rtpPeer, rtcpPeer: rtcpPeer,
		codec: config.Codec, frames: frames, buffer: time.Duration(config.BufferMS) * time.Millisecond,
		ssrc: config.SSRC, sequence: config.InitialSequence,
		timestamp: config.InitialTimestamp, rtcpGap: config.RTCPInterval,
		ctx: runContext, cancel: cancel, in: make(chan PCMFrame, capacity),
		out: make(chan PCMFrame, capacity), errors: make(chan error, 1), done: make(chan struct{}),
		peerReady:       make(chan struct{}),
		previousSilence: true,
	}
	if rtpPeer != nil {
		bridge.peerReadyOnce.Do(func() { close(bridge.peerReady) })
	}
	bridge.wait.Add(4)
	go bridge.sendRTP()
	go bridge.receiveRTP()
	go bridge.runRTCP()
	go func() {
		defer bridge.wait.Done()
		select {
		case <-ctx.Done():
			bridge.shutdown()
		case <-bridge.ctx.Done():
		}
	}()
	go func() {
		bridge.wait.Wait()
		bridge.frames.Close()
		bridge.closeOutputOnce.Do(func() { close(bridge.out) })
		close(bridge.errors)
		close(bridge.done)
	}()
	return bridge, nil
}

// LocalEndpoints returns the already-reserved userspace addresses for the SIP
// offer. Their ports remain owned by this Bridge until Close returns.
func (bridge *Bridge) LocalEndpoints() (rtpAddress, rtcpAddress net.Addr, err error) {
	if bridge == nil || bridge.ctx.Err() != nil {
		return nil, nil, ErrClosed
	}
	return bridge.rtpConn.LocalAddr(), bridge.rtcpConn.LocalAddr(), nil
}

// SetRemote applies an SDP answer without reopening local sockets. Re-INVITE
// endpoint updates are atomic; packets from the previous endpoint are rejected.
func (bridge *Bridge) SetRemote(rtpAddress, rtcpAddress string) error {
	if bridge == nil || bridge.ctx.Err() != nil {
		return ErrClosed
	}
	rtpPeer, err := udpEndpoint(rtpAddress)
	if err != nil {
		return fmt.Errorf("%w: remote RTP: %v", ErrInvalidConfig, err)
	}
	rtcpPeer, err := udpEndpoint(rtcpAddress)
	if err != nil {
		return fmt.Errorf("%w: remote RTCP: %v", ErrInvalidConfig, err)
	}
	bridge.mu.Lock()
	bridge.rtpPeer, bridge.rtcpPeer = rtpPeer, rtcpPeer
	bridge.remoteSeen, bridge.remoteCycles = false, 0
	bridge.mu.Unlock()
	bridge.peerReadyOnce.Do(func() { close(bridge.peerReady) })
	return nil
}

func (bridge *Bridge) PCM() <-chan PCMFrame {
	if bridge == nil {
		closed := make(chan PCMFrame)
		close(closed)
		return closed
	}
	return bridge.out
}

func (bridge *Bridge) Errors() <-chan error {
	if bridge == nil {
		closed := make(chan error)
		close(closed)
		return closed
	}
	return bridge.errors
}

// WritePCM accepts one existing browser-contract frame: 160 little-endian
// signed samples at 8 kHz. A stale or full queue drops only media, never the call.
func (bridge *Bridge) WritePCM(frame []byte, capturedAt time.Time) (bool, error) {
	if bridge == nil || bridge.ctx.Err() != nil {
		return false, ErrClosed
	}
	if len(frame) != PCMFrameBytes {
		return false, fmt.Errorf("%w: PCM frame must be %d bytes", ErrInvalidConfig, PCMFrameBytes)
	}
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	if time.Since(capturedAt) > bridge.buffer {
		bridge.changeStats(func(stats *Stats) { stats.BrowserFramesStale++ })
		return false, nil
	}
	owned := PCMFrame{Data: append([]byte(nil), frame...), CapturedAt: capturedAt}
	select {
	case <-bridge.ctx.Done():
		return false, ErrClosed
	case bridge.in <- owned:
		bridge.changeStats(func(stats *Stats) { stats.BrowserFramesAccepted++ })
		return true, nil
	default:
		bridge.changeStats(func(stats *Stats) { stats.BrowserFramesDropped++ })
		return false, nil
	}
}

func (bridge *Bridge) Stats() Stats {
	if bridge == nil {
		return Stats{}
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return bridge.stats
}

func (bridge *Bridge) Close(ctx context.Context) error {
	if bridge == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bridge.shutdown()
	select {
	case <-bridge.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (bridge *Bridge) sendRTP() {
	defer bridge.wait.Done()
	select {
	case <-bridge.ctx.Done():
		return
	case <-bridge.peerReady:
	}
	var frame PCMFrame

waitForFreshFrame:
	for {
		select {
		case <-bridge.ctx.Done():
			return
		case frame = <-bridge.in:
			if time.Since(frame.CapturedAt) <= bridge.buffer {
				break waitForFreshFrame
			}
			bridge.changeStats(func(stats *Stats) { stats.BrowserFramesStale++ })
		}
	}
	if !bridge.writeRTP(frame.Data, false) {
		return
	}
	ticker := time.NewTicker(FrameDuration)
	defer ticker.Stop()
	silence := make([]byte, PCMFrameBytes)
	for {
		select {
		case <-bridge.ctx.Done():
			return
		case <-ticker.C:
			frame = PCMFrame{Data: silence}
			underflow := true
			select {
			case candidate := <-bridge.in:
				if time.Since(candidate.CapturedAt) <= bridge.buffer {
					frame, underflow = candidate, false
				} else {
					bridge.changeStats(func(stats *Stats) { stats.BrowserFramesStale++ })
				}
			default:
			}
			if !bridge.writeRTP(frame.Data, underflow) {
				return
			}
		}
	}
}

func (bridge *Bridge) writeRTP(pcm []byte, silence bool) bool {
	select {
	case <-bridge.ctx.Done():
		return false
	case <-bridge.peerReady:
	}
	payload, err := bridge.frames.EncodePCM(pcm)
	if err != nil {
		bridge.fail(fmt.Errorf("encode RTP audio: %w", err))
		return false
	}
	bridge.mu.Lock()
	sequence, timestamp := bridge.sequence, bridge.timestamp
	marker := bridge.stats.RTPPacketsSent == 0 || (!silence && bridge.previousSilence)
	peer := bridge.rtpPeer
	bridge.mu.Unlock()
	packet := rtp.Packet{Header: rtp.Header{
		Version: 2, Marker: marker, PayloadType: bridge.codec.PayloadType(), SequenceNumber: sequence,
		Timestamp: timestamp, SSRC: bridge.ssrc,
	}, Payload: payload}
	wire, err := packet.Marshal()
	if err == nil {
		_, err = bridge.rtpConn.WriteTo(wire, peer)
	}
	if err != nil {
		if bridge.ctx.Err() == nil {
			bridge.fail(fmt.Errorf("write userspace RTP: %w", err))
		}
		return false
	}
	bridge.mu.Lock()
	bridge.sequence++
	bridge.timestamp += FrameSamples
	bridge.sentOctets += uint32(len(payload))
	bridge.previousSilence = silence
	bridge.stats.RTPPacketsSent++
	if silence {
		bridge.stats.SilenceFramesSent++
	}
	bridge.mu.Unlock()
	return true
}

func (bridge *Bridge) receiveRTP() {
	defer bridge.wait.Done()
	buffer := make([]byte, 65535)
	for {
		size, source, err := bridge.rtpConn.ReadFrom(buffer)
		if err != nil {
			if bridge.ctx.Err() == nil {
				bridge.fail(fmt.Errorf("read userspace RTP: %w", err))
			}
			return
		}
		if !bridge.sameRTPPeer(source) {
			bridge.changeStats(func(stats *Stats) { stats.RTPPacketsRejected++ })
			continue
		}
		var packet rtp.Packet
		if err := packet.Unmarshal(buffer[:size]); err != nil || packet.Version != 2 ||
			packet.PayloadType != bridge.codec.PayloadType() || bridge.frames.ValidateRTP(packet.Payload) != nil {
			bridge.changeStats(func(stats *Stats) { stats.RTPPacketsRejected++ })
			continue
		}
		if !bridge.acceptSequence(packet.SequenceNumber, packet.SSRC) {
			continue
		}
		pcm, err := bridge.frames.DecodeRTP(packet.Payload)
		if err != nil {
			bridge.changeStats(func(stats *Stats) { stats.RTPPacketsRejected++ })
			continue
		}
		frame := PCMFrame{Data: pcm, CapturedAt: time.Now()}
		select {
		case <-bridge.ctx.Done():
			return
		case bridge.out <- frame:
			bridge.changeStats(func(stats *Stats) {
				stats.RTPPacketsReceived++
				stats.PCMFramesDelivered++
			})
		default:
			// Match the browser AudioWorklet: discard oldest audio so playback catches up.
			select {
			case <-bridge.out:
			default:
			}
			select {
			case bridge.out <- frame:
				bridge.changeStats(func(stats *Stats) {
					stats.RTPPacketsReceived++
					stats.PCMFramesDelivered++
					stats.PCMFramesDropped++
				})
			case <-bridge.ctx.Done():
				return
			}
		}
	}
}

func (bridge *Bridge) acceptSequence(sequence uint16, ssrc uint32) bool {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if !bridge.remoteSeen || bridge.remoteSSRC != ssrc {
		bridge.remoteSeen, bridge.remoteSequence, bridge.remoteSSRC = true, sequence, ssrc
		bridge.remoteCycles = 0
		return true
	}
	delta := int16(sequence - bridge.remoteSequence)
	if delta <= 0 {
		bridge.stats.RTPPacketsRejected++
		return false
	}
	if delta > 1 {
		bridge.stats.RTPPacketsLost += uint64(delta - 1)
	}
	if sequence < bridge.remoteSequence {
		bridge.remoteCycles += 1 << 16
	}
	bridge.remoteSequence = sequence
	return true
}

func (bridge *Bridge) runRTCP() {
	defer bridge.wait.Done()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 65535)
		for {
			size, source, err := bridge.rtcpConn.ReadFrom(buffer)
			if err != nil {
				return
			}
			if !bridge.sameRTCPPeer(source) {
				continue
			}
			packets, err := rtcp.Unmarshal(buffer[:size])
			if err == nil {
				bridge.changeStats(func(stats *Stats) { stats.RTCPPacketsReceived += uint64(len(packets)) })
			}
		}
	}()
	select {
	case <-bridge.ctx.Done():
		<-readDone
		return
	case <-bridge.peerReady:
	}
	ticker := time.NewTicker(bridge.rtcpGap)
	defer ticker.Stop()
	for {
		select {
		case <-bridge.ctx.Done():
			<-readDone
			return
		case <-ticker.C:
			bridge.mu.Lock()
			seen, remoteSSRC := bridge.remoteSeen, bridge.remoteSSRC
			peer := bridge.rtcpPeer
			sequence := bridge.remoteCycles | uint32(bridge.remoteSequence)
			lost := bridge.stats.RTPPacketsLost
			packetCount, octetCount, rtpTime := uint32(bridge.stats.RTPPacketsSent), bridge.sentOctets, bridge.timestamp
			bridge.mu.Unlock()
			var reception []rtcp.ReceptionReport
			if seen {
				reception = []rtcp.ReceptionReport{{
					SSRC: remoteSSRC, TotalLost: uint32(min(lost, 0xFFFFFF)),
					LastSequenceNumber: sequence,
				}}
			}
			var report rtcp.Packet = &rtcp.ReceiverReport{SSRC: bridge.ssrc, Reports: reception}
			if packetCount > 0 {
				report = &rtcp.SenderReport{
					SSRC: bridge.ssrc, NTPTime: ntpTimestamp(time.Now()), RTPTime: rtpTime,
					PacketCount: packetCount, OctetCount: octetCount, Reports: reception,
				}
			}
			wire, err := report.Marshal()
			if err == nil {
				_, err = bridge.rtcpConn.WriteTo(wire, peer)
			}
			if err != nil {
				if bridge.ctx.Err() == nil {
					bridge.fail(fmt.Errorf("write userspace RTCP: %w", err))
				}
				<-readDone
				return
			}
			bridge.changeStats(func(stats *Stats) { stats.RTCPPacketsSent++ })
		}
	}
}

func (bridge *Bridge) fail(err error) {
	select {
	case bridge.errors <- err:
	default:
	}
	bridge.shutdown()
}

func (bridge *Bridge) shutdown() {
	bridge.shutdownOnce.Do(func() {
		bridge.cancel()
		_ = bridge.rtpConn.Close()
		_ = bridge.rtcpConn.Close()
	})
}

func (bridge *Bridge) changeStats(change func(*Stats)) {
	bridge.mu.Lock()
	change(&bridge.stats)
	bridge.mu.Unlock()
}

func udpEndpoint(value string) (*net.UDPAddr, error) {
	endpoint, err := netip.ParseAddrPort(value)
	if err != nil || !endpoint.Addr().IsValid() || endpoint.Port() == 0 {
		return nil, fmt.Errorf("must be a literal IP and non-zero port")
	}
	return net.UDPAddrFromAddrPort(endpoint), nil
}

func sameEndpoint(got net.Addr, want *net.UDPAddr) bool {
	if want == nil {
		return false
	}
	address, ok := got.(*net.UDPAddr)
	return ok && address.AddrPort() == want.AddrPort()
}

func (bridge *Bridge) sameRTPPeer(address net.Addr) bool {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return sameEndpoint(address, bridge.rtpPeer)
}

func (bridge *Bridge) sameRTCPPeer(address net.Addr) bool {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return sameEndpoint(address, bridge.rtcpPeer)
}

func ntpTimestamp(value time.Time) uint64 {
	const unixToNTP = 2208988800
	seconds := uint64(value.Unix() + unixToNTP)
	fraction := (uint64(value.Nanosecond()) << 32) / uint64(time.Second)
	return seconds<<32 | fraction
}
