// SPDX-License-Identifier: AGPL-3.0-only

package media

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/zaf/g711"
)

func TestBridgeCarriesBidirectionalNonSilentAudioOverUserspaceStack(t *testing.T) {
	clientStack, peerStack := openMediaStackPair(t)
	peerRTP, err := peerStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5000")
	if err != nil {
		t.Fatal(err)
	}
	defer peerRTP.Close()
	peerRTCP, err := peerStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5001")
	if err != nil {
		t.Fatal(err)
	}
	defer peerRTCP.Close()
	bridge, err := Open(context.Background(), clientStack, Config{
		LocalRTP: "10.0.0.1:4000", LocalRTCP: "10.0.0.1:4001",
		RemoteRTP: "10.0.0.2:5000", RemoteRTCP: "10.0.0.2:5001",
		Codec: CodecPCMU, BufferMS: 2000, SSRC: 0x10203040,
		InitialSequence: 100, InitialTimestamp: 1000, RTCPInterval: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })

	browserPCM := nonSilentPCM(12000)
	accepted, err := bridge.WritePCM(browserPCM, time.Now())
	if err != nil || !accepted {
		t.Fatalf("WritePCM() = %v, %v", accepted, err)
	}
	_ = peerRTP.SetReadDeadline(time.Now().Add(2 * time.Second))
	wire := make([]byte, 2048)
	size, source, err := peerRTP.ReadFrom(wire)
	if err != nil {
		t.Fatal(err)
	}
	var outbound rtp.Packet
	if err := outbound.Unmarshal(wire[:size]); err != nil {
		t.Fatal(err)
	}
	if outbound.PayloadType != 0 || outbound.SequenceNumber != 100 || outbound.Timestamp != 1000 ||
		outbound.SSRC != 0x10203040 || !hasSignal(g711.DecodeUlaw(outbound.Payload)) {
		t.Fatalf("outbound RTP = %+v, signal=%v", outbound.Header, hasSignal(g711.DecodeUlaw(outbound.Payload)))
	}

	response := rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 0, SequenceNumber: 900, Timestamp: 8000, SSRC: 0x55667788,
	}, Payload: g711.EncodeUlaw(nonSilentPCM(-9000))}
	responseWire, err := response.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peerRTP.WriteTo(responseWire, source); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-bridge.PCM():
		if len(received.Data) != PCMFrameBytes || !hasSignal(received.Data) {
			t.Fatalf("received PCM bytes=%d signal=%v", len(received.Data), hasSignal(received.Data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for IMS-to-browser PCM")
	}

	_ = peerRTCP.SetReadDeadline(time.Now().Add(2 * time.Second))
	var sender *rtcp.SenderReport
	for sender == nil || len(sender.Reports) == 0 {
		size, _, err = peerRTCP.ReadFrom(wire)
		if err != nil {
			t.Fatal(err)
		}
		reports, parseErr := rtcp.Unmarshal(wire[:size])
		if parseErr != nil || len(reports) != 1 {
			t.Fatalf("RTCP reports=%d error=%v", len(reports), parseErr)
		}
		sender, _ = reports[0].(*rtcp.SenderReport)
	}
	if sender.Reports[0].SSRC != 0x55667788 || sender.PacketCount == 0 || sender.OctetCount == 0 {
		t.Fatalf("RTCP sender report = %#v", sender)
	}

	closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := bridge.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	if accepted, err := bridge.WritePCM(browserPCM, time.Now()); accepted || !errors.Is(err, ErrClosed) {
		t.Fatalf("WritePCM(after close) = %v, %v", accepted, err)
	}
	drainPackets(peerRTP)
	_ = peerRTP.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	if _, _, err := peerRTP.ReadFrom(wire); err == nil || !isTimeout(err) {
		t.Fatalf("RTP after close error = %v, want timeout", err)
	}
	stats := bridge.Stats()
	if stats.BrowserFramesAccepted != 1 || stats.RTPPacketsSent == 0 ||
		stats.RTPPacketsReceived != 1 || stats.PCMFramesDelivered != 1 || stats.RTCPPacketsSent == 0 {
		t.Fatalf("bridge stats = %+v", stats)
	}
}

func TestBridgeReservesOfferPortsBeforeRemoteSDPIsKnown(t *testing.T) {
	clientStack, peerStack := openMediaStackPair(t)
	peerRTP, err := peerStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5200")
	if err != nil {
		t.Fatal(err)
	}
	defer peerRTP.Close()
	peerRTCP, err := peerStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5201")
	if err != nil {
		t.Fatal(err)
	}
	defer peerRTCP.Close()
	bridge, err := Open(context.Background(), clientStack, Config{
		LocalRTP: "10.0.0.1:4200", LocalRTCP: "10.0.0.1:4201",
		Codec: CodecPCMU, BufferMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close(context.Background())
	rtpAddress, rtcpAddress, err := bridge.LocalEndpoints()
	if err != nil || rtpAddress.String() != "10.0.0.1:4200" || rtcpAddress.String() != "10.0.0.1:4201" {
		t.Fatalf("LocalEndpoints() = %v, %v, %v", rtpAddress, rtcpAddress, err)
	}
	if accepted, err := bridge.WritePCM(nonSilentPCM(7000), time.Now()); err != nil || !accepted {
		t.Fatalf("WritePCM(before answer) = %v, %v", accepted, err)
	}
	_ = peerRTP.SetReadDeadline(time.Now().Add(60 * time.Millisecond))
	if _, _, err := peerRTP.ReadFrom(make([]byte, 2048)); err == nil || !isTimeout(err) {
		t.Fatalf("RTP escaped before remote SDP: %v", err)
	}
	if err := bridge.SetRemote("10.0.0.2:5200", "10.0.0.2:5201"); err != nil {
		t.Fatal(err)
	}
	_ = peerRTP.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := peerRTP.ReadFrom(make([]byte, 2048)); err != nil {
		t.Fatalf("reserved RTP socket did not use SDP answer: %v", err)
	}
}

func TestBridgeDropsStaleAndOverflowWithoutClosingMedia(t *testing.T) {
	clientStack, peerStack := openMediaStackPair(t)
	peerRTP, err := peerStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5100")
	if err != nil {
		t.Fatal(err)
	}
	defer peerRTP.Close()
	peerRTCP, err := peerStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5101")
	if err != nil {
		t.Fatal(err)
	}
	defer peerRTCP.Close()
	runContext, stop := context.WithCancel(context.Background())
	bridge, err := Open(runContext, clientStack, Config{
		LocalRTP: "10.0.0.1:4100", LocalRTCP: "10.0.0.1:4101",
		RemoteRTP: "10.0.0.2:5100", RemoteRTCP: "10.0.0.2:5101",
		Codec: CodecPCMA, BufferMS: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close(context.Background())
	accepted, err := bridge.WritePCM(nonSilentPCM(5000), time.Now().Add(-time.Second))
	if err != nil || accepted {
		t.Fatalf("stale WritePCM() = %v, %v", accepted, err)
	}
	if bridge.Stats().BrowserFramesStale != 1 {
		t.Fatalf("stats = %+v", bridge.Stats())
	}
	pcm := nonSilentPCM(5000)
	for index := 0; index < 20; index++ {
		if _, err := bridge.WritePCM(pcm, time.Now()); err != nil {
			t.Fatalf("WritePCM(%d) error = %v", index, err)
		}
	}
	if bridge.Stats().BrowserFramesDropped == 0 {
		t.Fatalf("bounded queue did not drop overflow: %+v", bridge.Stats())
	}
	select {
	case err := <-bridge.Errors():
		if err != nil {
			t.Fatalf("media queue pressure closed bridge: %v", err)
		}
	default:
	}
	stop()
	select {
	case _, open := <-bridge.PCM():
		if open {
			t.Fatal("PCM output remained open after parent cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not close media bridge")
	}
	if accepted, err := bridge.WritePCM(pcm, time.Now()); accepted || !errors.Is(err, ErrClosed) {
		t.Fatalf("WritePCM(after cancel) = %v, %v", accepted, err)
	}
}

func TestRemoteSequenceWrapProducesExtendedRTCPSequence(t *testing.T) {
	bridge := &Bridge{}
	if !bridge.acceptSequence(0xFFFF, 77) || !bridge.acceptSequence(0, 77) {
		t.Fatal("valid RTP wrap was rejected")
	}
	if got := bridge.remoteCycles | uint32(bridge.remoteSequence); got != 0x10000 {
		t.Fatalf("extended sequence = %#x, want 0x10000", got)
	}
}

func nonSilentPCM(value int16) []byte {
	pcm := make([]byte, PCMFrameBytes)
	for index := 0; index < FrameSamples; index++ {
		sample := value
		if index%2 == 1 {
			sample = -value
		}
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

func hasSignal(pcm []byte) bool {
	for index := 0; index+1 < len(pcm); index += 2 {
		value := int16(binary.LittleEndian.Uint16(pcm[index:]))
		if value > 1000 || value < -1000 {
			return true
		}
	}
	return false
}

func drainPackets(connection net.PacketConn) {
	buffer := make([]byte, 2048)
	for {
		_ = connection.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
		if _, _, err := connection.ReadFrom(buffer); err != nil {
			return
		}
	}
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

type linkedMediaSession struct {
	peer   *linkedMediaSession
	input  chan []byte
	done   chan struct{}
	closed sync.Once
}

func linkedMediaSessions() (*linkedMediaSession, *linkedMediaSession) {
	left := &linkedMediaSession{input: make(chan []byte, 256), done: make(chan struct{})}
	right := &linkedMediaSession{input: make(chan []byte, 256), done: make(chan struct{})}
	left.peer, right.peer = right, left
	return left, right
}

func (session *linkedMediaSession) Send(ctx context.Context, packet []byte) error {
	owned := append([]byte(nil), packet...)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.done:
		return io.EOF
	case <-session.peer.done:
		return io.EOF
	case session.peer.input <- owned:
		return nil
	}
}

func (session *linkedMediaSession) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-session.done:
		return nil, io.EOF
	case packet := <-session.input:
		return append([]byte(nil), packet...), nil
	}
}

func (session *linkedMediaSession) Close(context.Context) error {
	session.closed.Do(func() { close(session.done) })
	return nil
}

func openMediaStackPair(t *testing.T) (*usernet.Stack, *usernet.Stack) {
	t.Helper()
	leftPackets, rightPackets := linkedMediaSessions()
	left, err := usernet.Open(context.Background(), leftPackets, usernet.Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	right, err := usernet.Open(context.Background(), rightPackets, usernet.Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.2")},
	})
	if err != nil {
		_ = left.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = left.Close(closeContext)
		_ = right.Close(closeContext)
	})
	return left, right
}
