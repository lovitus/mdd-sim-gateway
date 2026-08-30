// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
	"github.com/pion/rtp"
)

type incomingTerminator struct {
	mu         sync.Mutex
	calls      []string
	dtmfCalls  []string
	dtmfSignal string
	result     voicehost.DialogInfoResult
	err        error
}

func (terminator *incomingTerminator) SendCarrierRTPDTMF(_ context.Context, callID, signal string, _ int) (voicehost.DialogRTPDTMFResult, error) {
	terminator.mu.Lock()
	defer terminator.mu.Unlock()
	terminator.dtmfCalls = append(terminator.dtmfCalls, callID)
	terminator.dtmfSignal = signal
	return voicehost.DialogRTPDTMFResult{Accepted: true, StatusCode: 200, Reason: "OK"}, nil
}

func (terminator *incomingTerminator) EndCarrierCallWithResult(_ context.Context, callID string) (voicehost.DialogInfoResult, error) {
	terminator.mu.Lock()
	defer terminator.mu.Unlock()
	terminator.calls = append(terminator.calls, callID)
	return terminator.result, terminator.err
}

func (terminator *incomingTerminator) count() int {
	terminator.mu.Lock()
	defer terminator.mu.Unlock()
	return len(terminator.calls)
}

type incomingInviteResult struct {
	response voiceclient.SIPResponse
	err      error
}

const incomingTestDeadline = 3 * time.Second

func TestIncomingCallAnswerCarriesBidirectionalUserspaceMediaAndEndsCarrierDialog(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	rtpConn, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5000")
	if err != nil {
		t.Fatal(err)
	}
	defer rtpConn.Close()
	rtcpConn, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5001")
	if err != nil {
		t.Fatal(err)
	}
	defer rtcpConn.Close()

	terminator := &incomingTerminator{result: voicehost.DialogInfoResult{
		Accepted: true, StatusCode: 200, Reason: "OK",
	}}
	controller := newTestIncomingController(t, clientStack, terminator)
	request := incomingInvite("incoming-media", 5000, 5001)
	provisional := make(chan voiceclient.SIPResponse, 1)
	final := make(chan incomingInviteResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		response, inviteErr := controller.RoundTripInvite(ctx, request, func(_ context.Context, _ voiceclient.SIPRequestMessage, response voiceclient.SIPResponse) error {
			provisional <- response
			return nil
		})
		final <- incomingInviteResult{response: response, err: inviteErr}
	}()

	ringing := receiveResponse(t, provisional)
	if ringing.StatusCode != 180 {
		t.Fatalf("provisional status = %d, want 180", ringing.StatusCode)
	}
	pending, ok := controller.Pending()
	if !ok || pending.CallID != "incoming-media" || pending.Caller != "sip:+44123@ims.test" || pending.Callee != "sip:+44999@ims.test" {
		t.Fatalf("Pending() = %+v, %v", pending, ok)
	}
	call, err := controller.Answer(ctx, pending.CallID, 500)
	if err != nil {
		t.Fatal(err)
	}
	answered := receiveInviteResult(t, final)
	if answered.err != nil || answered.response.StatusCode != 200 {
		t.Fatalf("answer = %+v, %v", answered.response, answered.err)
	}
	if route, err := call.SendDTMF(ctx, "5", 160); err != nil || route != voicehost.DialogDTMFRouteRTP {
		t.Fatalf("SendDTMF() route=%q err=%v", route, err)
	}
	terminator.mu.Lock()
	if len(terminator.dtmfCalls) != 1 || terminator.dtmfCalls[0] != pending.CallID || terminator.dtmfSignal != "5" {
		terminator.mu.Unlock()
		t.Fatalf("DTMF calls=%v signal=%q", terminator.dtmfCalls, terminator.dtmfSignal)
	}
	terminator.mu.Unlock()
	description, err := voicehost.ParseSDPMediaDescription(answered.response.Body)
	if err != nil || description.Info.ConnectionIP != "10.0.0.1" || description.Info.MediaPort == 0 {
		t.Fatalf("answer SDP = %+v, %v", description, err)
	}

	pcm := make([]byte, media.PCMFrameBytes)
	for index := 0; index < media.FrameSamples; index++ {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(int16(index*101-6500)))
	}
	accepted, err := call.WritePCM(pcm, time.Now())
	if err != nil || !accepted {
		t.Fatalf("WritePCM() = %v, %v", accepted, err)
	}
	outbound, clientRTP := receiveNonSilentRTP(t, rtpConn)
	if outbound.PayloadType != media.CodecPCMU.PayloadType() {
		t.Fatalf("outbound payload type = %d", outbound.PayloadType)
	}
	inbound := rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: outbound.PayloadType, SequenceNumber: 17, Timestamp: 160, SSRC: 91,
	}, Payload: append([]byte(nil), outbound.Payload...)}
	wire, err := inbound.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rtpConn.WriteTo(wire, clientRTP); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-call.PCM():
		if len(got.Data) != media.PCMFrameBytes || allZero(got.Data) {
			t.Fatalf("browser PCM was not non-silent: %d bytes", len(got.Data))
		}
	case <-time.After(incomingTestDeadline):
		t.Fatal("browser PCM was not delivered")
	}

	ended, err := call.End(ctx)
	if err != nil || !ended.Accepted || terminator.count() != 1 {
		t.Fatalf("End() = %+v, %v; carrier calls = %d", ended, err, terminator.count())
	}
	if _, err := call.WritePCM(pcm, time.Now()); !errors.Is(err, media.ErrClosed) {
		t.Fatalf("WritePCM() after end = %v, want ErrClosed", err)
	}
}

func TestIncomingCallRejectCancelAndAvailabilityAreBounded(t *testing.T) {
	clientStack, _ := openStackPair(t)
	controller := newTestIncomingController(t, clientStack, &incomingTerminator{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	controller.SetAvailability(func() bool { return false })
	response, err := controller.RoundTripInvite(ctx, incomingInvite("unavailable", 5000, 5001), nil)
	if err != nil || response.StatusCode != 486 {
		t.Fatalf("unavailable INVITE = %+v, %v", response, err)
	}
	controller.SetAvailability(nil)

	for _, test := range []struct {
		name       string
		callID     string
		resolve    func() (voiceclient.SIPResponse, error)
		wantStatus int
	}{
		{name: "reject", callID: "reject-me", wantStatus: 486},
		{name: "cancel", callID: "cancel-me", wantStatus: 487},
	} {
		t.Run(test.name, func(t *testing.T) {
			provisional := make(chan voiceclient.SIPResponse, 1)
			final := make(chan incomingInviteResult, 1)
			go func() {
				result, inviteErr := controller.RoundTripInvite(ctx, incomingInvite(test.callID, 5000, 5001), func(_ context.Context, _ voiceclient.SIPRequestMessage, response voiceclient.SIPResponse) error {
					provisional <- response
					return nil
				})
				final <- incomingInviteResult{response: result, err: inviteErr}
			}()
			if response := receiveResponse(t, provisional); response.StatusCode != 180 {
				t.Fatalf("provisional status = %d", response.StatusCode)
			}
			if test.name == "reject" {
				if err := controller.Reject(test.callID); err != nil {
					t.Fatal(err)
				}
			} else {
				cancelResponse, cancelErr := controller.RoundTripRequest(ctx, voiceclient.SIPRequestMessage{
					Method: "CANCEL", Headers: map[string]string{"Call-ID": test.callID},
				})
				if cancelErr != nil || cancelResponse.StatusCode != 200 {
					t.Fatalf("CANCEL = %+v, %v", cancelResponse, cancelErr)
				}
			}
			result := receiveInviteResult(t, final)
			if result.err != nil || result.response.StatusCode != test.wantStatus {
				t.Fatalf("final = %+v, %v", result.response, result.err)
			}
			if _, err := controller.Answer(ctx, test.callID, 500); !errors.Is(err, ErrIncomingCallNotFound) {
				t.Fatalf("Answer() after resolution = %v", err)
			}
		})
	}
}

func TestIncomingCallHasOneOwnerAndRemoteByeStopsMedia(t *testing.T) {
	clientStack, _ := openStackPair(t)
	terminator := &incomingTerminator{result: voicehost.DialogInfoResult{Accepted: true, StatusCode: 200}}
	controller := newTestIncomingController(t, clientStack, terminator)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provisional := make(chan voiceclient.SIPResponse, 1)
	final := make(chan incomingInviteResult, 1)
	go func() {
		response, inviteErr := controller.RoundTripInvite(ctx, incomingInvite("one-owner", 5000, 5001), func(_ context.Context, _ voiceclient.SIPRequestMessage, response voiceclient.SIPResponse) error {
			provisional <- response
			return nil
		})
		final <- incomingInviteResult{response: response, err: inviteErr}
	}()
	_ = receiveResponse(t, provisional)

	if response, err := controller.RoundTripInvite(ctx, incomingInvite("other-call", 5000, 5001), nil); err != nil || response.StatusCode != 486 {
		t.Fatalf("second INVITE = %+v, %v", response, err)
	}
	type answerResult struct {
		call *InboundMediaCall
		err  error
	}
	answers := make(chan answerResult, 2)
	for range 2 {
		go func() {
			call, answerErr := controller.Answer(ctx, "one-owner", 500)
			answers <- answerResult{call: call, err: answerErr}
		}()
	}
	var call *InboundMediaCall
	losers := 0
	for range 2 {
		result := <-answers
		if result.err == nil {
			if call != nil {
				t.Fatal("two Answer calls acquired the same incoming call")
			}
			call = result.call
		} else if errors.Is(result.err, ErrIncomingCallBusy) || errors.Is(result.err, ErrIncomingCallNotFound) {
			losers++
		} else {
			t.Fatalf("unexpected Answer error: %v", result.err)
		}
	}
	if call == nil || losers != 1 {
		t.Fatalf("answer ownership = call %v, losers %d", call != nil, losers)
	}
	if result := receiveInviteResult(t, final); result.err != nil || result.response.StatusCode != 200 {
		t.Fatalf("final = %+v, %v", result.response, result.err)
	}

	bye, err := controller.RoundTripRequest(ctx, voiceclient.SIPRequestMessage{
		Method: "BYE", Headers: map[string]string{"Call-ID": "one-owner"},
	})
	if err != nil || bye.StatusCode != 200 {
		t.Fatalf("remote BYE = %+v, %v", bye, err)
	}
	select {
	case <-call.RemoteEnded():
	case <-time.After(incomingTestDeadline):
		t.Fatal("remote end was not published")
	}
	select {
	case err := <-call.Errors():
		if !errors.Is(err, ErrIncomingCallEnded) {
			t.Fatalf("remote end error = %v", err)
		}
	case <-time.After(incomingTestDeadline):
		t.Fatal("remote end reason was not published")
	}
	ended, err := call.End(ctx)
	if err != nil || !ended.Accepted || terminator.count() != 0 {
		t.Fatalf("End() after remote BYE = %+v, %v; carrier calls = %d", ended, err, terminator.count())
	}
}

func TestIncomingCallAnswerCompletesCarrierDecisionAfterBrowserContextCancellation(t *testing.T) {
	clientStack, _ := openStackPair(t)
	terminator := &incomingTerminator{result: voicehost.DialogInfoResult{Accepted: true, StatusCode: 200}}
	controller := newTestIncomingController(t, clientStack, terminator)
	flowContext, cancelFlow := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFlow()
	provisional := make(chan voiceclient.SIPResponse, 1)
	final := make(chan incomingInviteResult, 1)
	go func() {
		response, inviteErr := controller.RoundTripInvite(flowContext, incomingInvite("answer-canceled-http", 5000, 5001), func(_ context.Context, _ voiceclient.SIPRequestMessage, response voiceclient.SIPResponse) error {
			provisional <- response
			return nil
		})
		final <- incomingInviteResult{response: response, err: inviteErr}
	}()
	_ = receiveResponse(t, provisional)

	answerContext, cancelAnswer := context.WithCancel(context.Background())
	cancelAnswer()
	call, err := controller.Answer(answerContext, "answer-canceled-http", 500)
	if err != nil || call == nil {
		t.Fatalf("Answer(canceled context) call=%v err=%v", call != nil, err)
	}
	result := receiveInviteResult(t, final)
	if result.err != nil || result.response.StatusCode != 200 {
		t.Fatalf("carrier final after browser cancellation = %+v, %v", result.response, result.err)
	}
	if _, err := call.End(context.Background()); err != nil {
		t.Fatalf("End() error = %v", err)
	}
}

func newTestIncomingController(t *testing.T, stack *usernet.Stack, terminator IncomingCarrierTerminator) *IncomingCallController {
	t.Helper()
	controller, err := NewIncomingCallController(stack, "10.0.0.1", "sip:mdd@10.0.0.1", "mdd-local")
	if err != nil {
		t.Fatal(err)
	}
	controller.SetCarrierTerminator(terminator)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	return controller
}

func incomingInvite(callID string, rtpPort, rtcpPort int) voiceclient.SIPRequestMessage {
	body := voicehost.BuildSDPAnswerWithOptions(voicehost.SDPInfo{
		ConnectionIP: "10.0.0.2", MediaPort: rtpPort,
		RTCPIP: "10.0.0.2", RTCPPort: rtcpPort,
		Direction: "sendrecv", PTimeMS: 20, MaxPTimeMS: 20,
	}, voicehost.SDPAnswerOptions{
		Codecs: []voicehost.SDPCodec{voicehost.NewSDPPCMUCodec()}, PTimeMS: 20, MaxPTimeMS: 20,
	})
	return voiceclient.SIPRequestMessage{
		Method: "INVITE", URI: "sip:+44999@ims.test",
		Headers: map[string]string{
			"Call-ID": callID,
			"From":    `"Caller" <sip:+44123@ims.test>;tag=remote`,
			"To":      "<sip:+44999@ims.test>",
		},
		Body: body,
	}
}

func receiveResponse(t *testing.T, channel <-chan voiceclient.SIPResponse) voiceclient.SIPResponse {
	t.Helper()
	select {
	case response := <-channel:
		return response
	case <-time.After(incomingTestDeadline):
		t.Fatal("timed out waiting for SIP response")
		return voiceclient.SIPResponse{}
	}
}

func receiveInviteResult(t *testing.T, channel <-chan incomingInviteResult) incomingInviteResult {
	t.Helper()
	select {
	case result := <-channel:
		return result
	case <-time.After(incomingTestDeadline):
		t.Fatal("timed out waiting for final INVITE response")
		return incomingInviteResult{}
	}
}

func receiveNonSilentRTP(t *testing.T, connection net.PacketConn) (rtp.Packet, net.Addr) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(incomingTestDeadline)); err != nil {
		t.Fatal(err)
	}
	wire := make([]byte, 2048)
	for {
		size, source, err := connection.ReadFrom(wire)
		if err != nil {
			t.Fatal(err)
		}
		var packet rtp.Packet
		if err := packet.Unmarshal(wire[:size]); err != nil {
			continue
		}
		nonSilent := false
		for _, sample := range packet.Payload {
			if sample != 0xff {
				nonSilent = true
				break
			}
		}
		if nonSilent {
			return packet, source
		}
	}
}
