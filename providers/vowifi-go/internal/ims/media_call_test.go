// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/pion/rtp"
)

func TestMediaCallBindsInviteAnswerAndByeToUserspaceMedia(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	mediaConn, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5000")
	if err != nil {
		t.Fatal(err)
	}
	defer mediaConn.Close()

	agent, registration, requests, serverDone := registeredVoiceFixture(t, clientStack, serverStack, []int{200})
	startCtx, cancelStart := context.WithTimeout(context.Background(), 5*time.Second)
	call, started, err := StartMediaCall(startCtx, agent, clientStack, MediaCallConfig{
		LocalRTP: "10.0.0.1:0", LocalRTCP: "10.0.0.1:0",
		Codec: media.CodecPCMU, BufferMS: 500,
	}, voicehost.OutboundCallRequest{
		DeviceID: "device-media", CallID: "call-media", Callee: "+100",
	})
	if err != nil || call == nil || !started.Accepted || started.StatusCode != 200 {
		t.Fatalf("StartMediaCall() = %v, %+v, %v", call, started, err)
	}
	cancelStart()

	pcm := make([]byte, media.PCMFrameBytes)
	for index := 0; index < media.FrameSamples; index++ {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(int16(index*97-7000)))
	}
	accepted, err := call.WritePCM(pcm, time.Now())
	if err != nil || !accepted {
		t.Fatalf("WritePCM() = %v, %v", accepted, err)
	}
	if err := mediaConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	wire := make([]byte, 2048)
	size, clientRTP, err := mediaConn.ReadFrom(wire)
	if err != nil {
		t.Fatal(err)
	}
	var outbound rtp.Packet
	if err := outbound.Unmarshal(wire[:size]); err != nil {
		t.Fatal(err)
	}
	if outbound.PayloadType != 0 || len(outbound.Payload) != media.FrameSamples {
		t.Fatalf("outbound RTP = PT %d, payload %d", outbound.PayloadType, len(outbound.Payload))
	}

	inbound := rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 0, SequenceNumber: 17, Timestamp: 160, SSRC: 91,
	}, Payload: append([]byte(nil), outbound.Payload...)}
	inboundWire, err := inbound.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mediaConn.WriteTo(inboundWire, clientRTP); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-call.PCM():
		if len(got.Data) != media.PCMFrameBytes || allZero(got.Data) {
			t.Fatalf("browser PCM was not non-silent: %d bytes", len(got.Data))
		}
	case <-time.After(time.Second):
		t.Fatal("browser PCM was not delivered")
	}

	endCtx, cancelEnd := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelEnd()
	ended, err := call.End(endCtx)
	if err != nil || !ended.Accepted || ended.StatusCode != 200 {
		t.Fatalf("End() = %+v, %v", ended, err)
	}
	if _, err := call.WritePCM(pcm, time.Now()); !errors.Is(err, media.ErrClosed) {
		t.Fatalf("WritePCM() after BYE error = %v, want ErrClosed", err)
	}
	drainPackets(mediaConn)
	if err := mediaConn.SetReadDeadline(time.Now().Add(80 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mediaConn.ReadFrom(wire); err == nil || !timeoutError(err) {
		t.Fatalf("RTP continued after BYE: %v", err)
	}

	finishVoiceFixture(t, registration, requests, serverDone,
		[]string{"REGISTER", "INVITE", "ACK", "BYE", "REGISTER"})
}

func TestMediaCallEndsAcceptedDialogWhenAnswerCannotBeUsed(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	agent, registration, requests, serverDone := registeredVoiceFixture(t, clientStack, serverStack, []int{200},
		"a=ptime:30\r\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	call, result, err := StartMediaCall(ctx, agent, clientStack, MediaCallConfig{
		LocalRTP: "10.0.0.1:0", LocalRTCP: "10.0.0.1:0",
		Codec: media.CodecPCMU, BufferMS: 500,
	}, voicehost.OutboundCallRequest{DeviceID: "device-media", CallID: "bad-media", Callee: "+100"})
	if call != nil || !result.Accepted || !errors.Is(err, ErrMediaNegotiation) || !strings.Contains(err.Error(), "ptime") {
		t.Fatalf("StartMediaCall() = %v, %+v, %v", call, result, err)
	}
	finishVoiceFixture(t, registration, requests, serverDone,
		[]string{"REGISTER", "INVITE", "ACK", "BYE", "REGISTER"})
}

func TestMediaCallClosesMediaWhenByeIsRejected(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	agent, registration, requests, serverDone := registeredVoiceFixture(t, clientStack, serverStack, []int{503, 200})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	call, result, err := StartMediaCall(ctx, agent, clientStack, MediaCallConfig{
		LocalRTP: "10.0.0.1:0", LocalRTCP: "10.0.0.1:0",
		Codec: media.CodecPCMU, BufferMS: 500,
	}, voicehost.OutboundCallRequest{DeviceID: "device-media", CallID: "bye-rejected", Callee: "+100"})
	if err != nil || call == nil || !result.Accepted {
		t.Fatalf("StartMediaCall() = %v, %+v, %v", call, result, err)
	}
	ended, err := call.End(ctx)
	if err == nil || ended.StatusCode != 503 {
		t.Fatalf("End() = %+v, %v", ended, err)
	}
	if _, err := call.WritePCM(make([]byte, media.PCMFrameBytes), time.Now()); !errors.Is(err, media.ErrClosed) {
		t.Fatalf("media remained writable after rejected BYE: %v", err)
	}
	ended, err = call.End(ctx)
	if err != nil || !ended.Accepted || ended.StatusCode != 200 {
		t.Fatalf("second End() = %+v, %v", ended, err)
	}
	finishVoiceFixture(t, registration, requests, serverDone,
		[]string{"REGISTER", "INVITE", "ACK", "BYE", "BYE", "REGISTER"})
}

func TestAMRMediaContractUsesDynamicBandwidthEfficientPayload(t *testing.T) {
	codec := sdpCodec(media.CodecAMR)
	if codec.Payload != 96 || codec.EncodingName != voicehost.SDPCodecAMR || codec.ClockRate != 8000 || codec.FMTP != "" {
		t.Fatalf("AMR SDP codec=%+v", codec)
	}
	answer := voicehost.OutboundCallResult{RawSDP: []byte(
		"v=0\r\nc=IN IP4 203.0.113.10\r\nm=audio 49170 RTP/AVP 96\r\n" +
			"a=rtpmap:96 AMR/8000\r\na=rtcp:49171 IN IP4 203.0.113.10\r\n",
	)}
	if _, _, err := acceptedMediaEndpoints(answer, media.CodecAMR); err != nil {
		t.Fatalf("bandwidth-efficient AMR answer rejected: %v", err)
	}
	answer.RawSDP = append(answer.RawSDP, []byte("a=fmtp:96 octet-align=1\r\n")...)
	if _, _, err := acceptedMediaEndpoints(answer, media.CodecAMR); !errors.Is(err, ErrMediaNegotiation) {
		t.Fatalf("octet-aligned AMR answer error=%v, want ErrMediaNegotiation", err)
	}
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

func drainPackets(connection net.PacketConn) {
	buffer := make([]byte, 2048)
	for {
		_ = connection.SetReadDeadline(time.Now().Add(time.Millisecond))
		if _, _, err := connection.ReadFrom(buffer); err != nil {
			return
		}
	}
}

func timeoutError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
