// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

type incomingMediaHarness struct {
	controller *IncomingCallController
	call       *InboundMediaCall
	request    voiceclient.SIPRequestMessage
	answer     voiceclient.SIPResponse
	peer       net.PacketConn
	rtcp       net.PacketConn
	pcm        []byte
}

func openIncomingMediaHarness(t *testing.T, payloads, attributes string) incomingMediaHarness {
	t.Helper()
	client, server := openStackPair(t)
	peer, err := server.ListenPacket(t.Context(), "udp4", "10.0.0.2:5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	rtcpPeer, err := server.ListenPacket(t.Context(), "udp4", "10.0.0.2:5001")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rtcpPeer.Close() })
	controller := newTestIncomingController(t, client, &incomingTerminator{result: voicehost.DialogInfoResult{Accepted: true, StatusCode: 200}})
	request := incomingInvite("hold-test", 5000, 5001)
	request.Headers["CSeq"] = "1 INVITE"
	request.Body = incomingNegotiationSDP(payloads, attributes)
	ringing := make(chan voiceclient.SIPResponse, 1)
	final := make(chan incomingInviteResult, 1)
	go func() {
		response, err := controller.RoundTripInvite(t.Context(), request, func(_ context.Context, _ voiceclient.SIPRequestMessage, response voiceclient.SIPResponse) error {
			ringing <- response
			return nil
		})
		final <- incomingInviteResult{response: response, err: err}
	}()
	if response := receiveResponse(t, ringing); response.StatusCode != 180 {
		t.Fatalf("ringing %d", response.StatusCode)
	}
	call, err := controller.Answer(t.Context(), "hold-test", 500)
	if err != nil {
		t.Fatal(err)
	}
	answer := receiveInviteResult(t, final)
	if answer.err != nil || answer.response.StatusCode != 200 {
		t.Fatalf("answer %+v", answer)
	}
	pcm := make([]byte, media.PCMFrameBytes)
	for i := 0; i < media.FrameSamples; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(i*101-6500)))
	}
	return incomingMediaHarness{controller, call, request, answer.response, peer, rtcpPeer, pcm}
}

func (h incomingMediaHarness) reinvite(body []byte, sequence int) voiceclient.SIPRequestMessage {
	request := h.request
	request.Headers = maps.Clone(h.request.Headers)
	request.Headers["CSeq"] = fmt.Sprintf("%d INVITE", sequence)
	request.Headers["To"] = h.answer.Headers["To"][0]
	request.Body = []byte(strings.Replace(string(body), "o=test 1 1 ", fmt.Sprintf("o=test 1 %d ", sequence), 1))
	return request
}

func readIncomingRTP(t *testing.T, peer net.PacketConn) (rtp.Packet, net.Addr) {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 2048)
	n, source, err := peer.ReadFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	var packet rtp.Packet
	if err := packet.Unmarshal(buffer[:n]); err != nil {
		t.Fatal(err)
	}
	return packet, source
}

func expectIncomingQuiet(t *testing.T, peer net.PacketConn) {
	t.Helper()
	// Drain packets already in flight at the update boundary, then require a
	// complete quiet interval. Continuous new packets fail within a fixed bound.
	for packets := 0; packets < 30; packets++ {
		_ = peer.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
		_, _, err := peer.ReadFrom(make([]byte, 2048))
		if err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				return
			}
			t.Fatal(err)
		}
	}
	t.Fatal("RTP continued while held")
}

func sdpOrigin(t *testing.T, body []byte) (string, uint64) {
	t.Helper()
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "o=") {
			fields := strings.Fields(line)
			if len(fields) != 6 {
				t.Fatal("invalid origin")
			}
			version, err := strconv.ParseUint(fields[2], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return fields[1], version
		}
	}
	t.Fatal("missing SDP origin")
	return "", 0
}

func TestIncomingReinviteHoldResumePreservesMedia(t *testing.T) {
	for _, tc := range []struct {
		name, payloads, attributes string
		payload                    uint8
	}{
		{"pcmu", "0", "a=rtpmap:0 PCMU/8000\r\n", 0},
		{"amr", "102", "a=rtpmap:102 AMR/8000\r\na=fmtp:102 mode-set=0,2,7\r\n", 102},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := openIncomingMediaHarness(t, tc.payloads, tc.attributes)
			if ok, err := h.call.WritePCM(h.pcm, time.Now()); err != nil || !ok {
				t.Fatalf("write %t %v", ok, err)
			}
			original, source := readIncomingRTP(t, h.peer)
			session, version := sdpOrigin(t, h.answer.Body)
			firstDescription, _ := voicehost.ParseSDPMediaDescription(h.answer.Body)
			for i, direction := range []string{"sendonly", "inactive", "recvonly", "sendrecv"} {
				request := h.reinvite([]byte(strings.ReplaceAll(string(h.request.Body), "a=sendrecv", "a="+direction)), i*2+2)
				answer, err := h.controller.RoundTripInvite(t.Context(), request, nil)
				if err != nil || answer.StatusCode != 200 {
					t.Fatalf("%s rejected: %+v %v", direction, answer, err)
				}
				desc, err := voicehost.ParseSDPMediaDescription(answer.Body)
				localDirection, _ := voicehost.SelectSDPAnswerDirection(direction, "sendrecv")
				if err != nil || desc.Info.Direction != localDirection || len(desc.Codecs) != 1 || desc.Codecs[0] != firstDescription.Codecs[0] || desc.Info.MediaPort != firstDescription.Info.MediaPort {
					t.Fatalf("%s changed contract %+v %v", direction, desc, err)
				}
				id, next := sdpOrigin(t, answer.Body)
				if id != session || next <= version {
					t.Fatalf("origin %s/%d after %s/%d", id, next, session, version)
				}
				version = next
				repeated, _ := h.controller.RoundTripInvite(t.Context(), request, nil)
				if !bytes.Equal(repeated.Body, answer.Body) {
					t.Fatal("repeated offer changed answer")
				}
				// A rejected update cannot unhold the call or move its media peer.
				bad := h.reinvite([]byte(strings.ReplaceAll(string(incomingNegotiationSDP("127", "a=rtpmap:127 PCMU/8000\r\n")), "5000", "5990")), i*2+3)
				if rejected, _ := h.controller.RoundTripInvite(t.Context(), bad, nil); rejected.StatusCode != 488 {
					t.Fatal("changed mapping accepted")
				}
				send := localDirection == "sendrecv" || localDirection == "sendonly"
				receive := localDirection == "sendrecv" || localDirection == "recvonly"
				ok, err := h.call.WritePCM(h.pcm, time.Now())
				if err != nil || ok != send {
					t.Fatalf("%s PCM admission %t %v", direction, ok, err)
				}
				if send {
					packet, _ := readIncomingRTP(t, h.peer)
					if packet.PayloadType != tc.payload || packet.SSRC != original.SSRC {
						t.Fatal("resume changed RTP identity")
					}
				} else {
					expectIncomingQuiet(t, h.peer)
				}
				packet := original
				packet.SSRC, packet.SequenceNumber = 91, uint16(i+1)
				wire, _ := packet.Marshal()
				if _, err := h.peer.WriteTo(wire, source); err != nil {
					t.Fatal(err)
				}
				if receive {
					select {
					case frame := <-h.call.PCM():
						if len(frame.Data) != media.PCMFrameBytes {
							t.Fatal("invalid PCM")
						}
					case <-time.After(time.Second):
						t.Fatal("permitted receive missing")
					}
				} else {
					select {
					case <-h.call.PCM():
						t.Fatal("received audio while direction forbids it")
					case <-time.After(100 * time.Millisecond):
					}
				}
				if direction == "inactive" {
					// RTCP remains live even when both audio directions are held.
					_ = h.rtcp.SetReadDeadline(time.Now().Add(6 * time.Second))
					buffer := make([]byte, 2048)
					n, _, err := h.rtcp.ReadFrom(buffer)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := rtcp.Unmarshal(buffer[:n]); err != nil {
						t.Fatal(err)
					}
				}
			}
			if ended, err := h.call.End(t.Context()); err != nil || !ended.Accepted {
				t.Fatalf("end %+v %v", ended, err)
			}
		})
	}
}

func TestIncomingDTMFUsesNegotiatedSocketAndHoldBoundary(t *testing.T) {
	h := openIncomingMediaHarness(t, "0 110", "a=rtpmap:0 PCMU/8000\r\na=rtpmap:110 telephone-event/8000\r\na=fmtp:110 0-9\r\n")
	desc, err := voicehost.ParseSDPMediaDescription(h.answer.Body)
	if err != nil || len(desc.Codecs) != 2 || desc.Codecs[1].Payload != 110 || desc.Codecs[1].FMTP != "0,1,2,3,4,5,6,7,8,9" {
		t.Fatalf("no negotiated events %+v %v", desc, err)
	}
	if _, err := h.call.SendDTMF(t.Context(), "#", 100); err == nil {
		t.Fatal("unnegotiated event sent")
	}
	result := make(chan error, 1)
	go func() { _, err := h.call.SendDTMF(t.Context(), "5", 400); result <- err }()
	first, source := readIncomingRTP(t, h.peer)
	if first.PayloadType != 110 || len(first.Payload) != 4 || first.Payload[0] != 5 {
		t.Fatalf("DTMF %+v", first)
	}
	if source.String() != fmt.Sprintf("10.0.0.1:%d", desc.Info.MediaPort) {
		t.Fatal("DTMF used another socket")
	}
	request := h.reinvite([]byte(strings.ReplaceAll(string(h.request.Body), "a=sendrecv", "a=inactive")), 2)
	if answer, _ := h.controller.RoundTripInvite(t.Context(), request, nil); answer.StatusCode != 200 {
		t.Fatal("hold rejected")
	}
	resumed, _ := h.controller.RoundTripInvite(t.Context(), h.reinvite(h.request.Body, 3), nil)
	if resumed.StatusCode != 200 {
		t.Fatal("resume rejected")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("old event survived hold/resume")
		}
	case <-time.After(time.Second):
		t.Fatal("DTMF blocked hold/end")
	}
	expectIncomingQuiet(t, h.peer)
	// A new explicit key is a new event; RFC4733 end packet copies are not retries.
	stamp := first.Timestamp
	for key := 0; key < 2; key++ {
		go func() { _, err := h.call.SendDTMF(t.Context(), "6", 80); result <- err }()
		ends := 0
		var sequence uint16
		for packets := 0; packets < 20 && ends < 3; packets++ {
			packet, _ := readIncomingRTP(t, h.peer)
			if packet.PayloadType != 110 || packet.SSRC != first.SSRC || len(packet.Payload) != 4 || packet.Payload[0] != 6 {
				t.Fatal("event identity changed")
			}
			if packets == 0 {
				if int32(packet.Timestamp-stamp) <= 0 {
					t.Fatal("distinct key reused previous event timestamp without PCM")
				}
				stamp = packet.Timestamp
				if key == 0 {
					refresh, err := h.controller.RoundTripInvite(t.Context(), h.reinvite(h.request.Body, 4), nil)
					if err != nil || refresh.StatusCode != 200 || !bytes.Equal(refresh.Body, resumed.Body) {
						t.Fatal("no-op refresh changed the local session description")
					}
				}
			} else if packet.SequenceNumber != sequence+1 {
				t.Fatal("event sequence did not advance")
			}
			sequence = packet.SequenceNumber
			if packet.Timestamp != stamp {
				t.Fatal("duplicate key event")
			}
			if packet.Payload[1]&0x80 != 0 {
				if binary.BigEndian.Uint16(packet.Payload[2:]) != 640 {
					t.Fatal("event ended with wrong duration")
				}
				ends++
			}
		}
		if ends != 3 {
			t.Fatal("event termination missing")
		}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := h.call.WritePCM(h.pcm, time.Now()); err != nil || !ok {
		t.Fatalf("start PCM %t %v", ok, err)
	}
	voice, _ := readIncomingRTP(t, h.peer)
	if voice.PayloadType != 0 || voice.SSRC != first.SSRC || int32(voice.Timestamp-(stamp+640)) < 0 {
		t.Fatal("audio clock moved backwards after events")
	}
	// Omitting the optional codec disables it until the same mapping is offered.
	request = h.reinvite(incomingNegotiationSDP("0", "a=rtpmap:0 PCMU/8000\r\n"), 5)
	if answer, _ := h.controller.RoundTripInvite(t.Context(), request, nil); answer.StatusCode != 200 {
		t.Fatal("event omission rejected")
	}
	if _, err := h.call.SendDTMF(t.Context(), "5", 100); err == nil {
		t.Fatal("omitted event sent")
	}
	if answer, _ := h.controller.RoundTripInvite(t.Context(), h.reinvite(h.request.Body, 6), nil); answer.StatusCode != 200 {
		t.Fatal("original event mapping could not return")
	}
	bad := h.reinvite([]byte(strings.ReplaceAll(string(h.request.Body), "110", "111")), 7)
	if answer, _ := h.controller.RoundTripInvite(t.Context(), bad, nil); answer.StatusCode != 488 {
		t.Fatal("event PT changed")
	}
	if ended, err := h.call.End(t.Context()); err != nil || !ended.Accepted {
		t.Fatalf("end %+v %v", ended, err)
	}
}
