// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/pion/rtp"
)

func TestIncomingNegotiatedPayloadCarriesBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name, payloads, attributes string
		payload                    uint8
		codec                      string
		rtcpPort                   int
		modeSet                    string
	}{
		{"amr_dynamic", "102", "a=rtpmap:102 AMR/8000\r\n", 102, "AMR", 5001, ""},
		{"amr_over_pcmu", "104 0", "a=rtpmap:104 AMR/8000\r\na=rtpmap:0 PCMU/8000\r\n", 104, "AMR", 5001, ""},
		{"pcmu_unchanged", "0", "a=rtpmap:0 PCMU/8000\r\n", 0, "PCMU", 5001, ""},
		{"amr_wb_and_nb_mux", "97 118", "a=rtpmap:97 AMR-WB/16000\r\na=rtpmap:118 AMR/8000\r\na=rtcp-mux\r\n", 118, "AMR", 5001, ""},
		{"mux_explicit_rtcp", "102", "a=rtpmap:102 AMR/8000\r\na=rtcp:5011 IN IP4 10.0.0.2\r\na=rtcp-mux\r\n", 102, "AMR", 5011, ""},
		{"interleaving_falls_back_pcmu", "102 0", "a=rtpmap:102 AMR/8000\r\na=fmtp:102 interleaving=0\r\n", 0, "PCMU", 5001, ""},
		{"interleaving_falls_back_amr", "102 104", "a=rtpmap:102 AMR/8000\r\na=fmtp:102 interleaving=0\r\na=rtpmap:104 AMR/8000\r\n", 104, "AMR", 5001, ""},
		{"mode_set_zero", "102", "a=rtpmap:102 AMR/8000\r\na=fmtp:102 mode-set=0\r\n", 102, "AMR", 5001, "0"},
		{"mode_period_neighbors", "102", "a=rtpmap:102 AMR/8000\r\na=fmtp:102 mode-set=0,2,7;mode-change-period=2;mode-change-neighbor=1\r\n", 102, "AMR", 5001, "0,2,7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := openStackPair(t)
			connection, err := server.ListenPacket(t.Context(), "udp4", "10.0.0.2:5000")
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			terminator := &incomingTerminator{result: voicehost.DialogInfoResult{Accepted: true, StatusCode: 200}}
			controller := newTestIncomingController(t, client, terminator)
			request := incomingInvite(tc.name, 5000, 5001)
			request.Body = incomingNegotiationSDP(tc.payloads, tc.attributes)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ringing := make(chan voiceclient.SIPResponse, 1)
			final := make(chan incomingInviteResult, 1)
			go func() {
				response, err := controller.RoundTripInvite(ctx, request, func(_ context.Context, _ voiceclient.SIPRequestMessage, response voiceclient.SIPResponse) error {
					ringing <- response
					return nil
				})
				final <- incomingInviteResult{response: response, err: err}
			}()
			select {
			case response := <-ringing:
				if response.StatusCode != 180 {
					t.Fatalf("ringing = %d", response.StatusCode)
				}
			case result := <-final:
				t.Fatalf("compatible offer rejected: %d %v", result.response.StatusCode, result.err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			controller.mu.Lock()
			remoteRTCP := controller.pending.remoteRTCP
			controller.mu.Unlock()
			if remoteRTCP != fmt.Sprintf("10.0.0.2:%d", tc.rtcpPort) {
				t.Fatalf("RTCP = %s", remoteRTCP)
			}
			call, err := controller.Answer(ctx, tc.name, 500)
			if err != nil {
				t.Fatal(err)
			}
			answer := receiveInviteResult(t, final)
			desc, err := voicehost.ParseSDPMediaDescription(answer.response.Body)
			if err != nil || answer.response.StatusCode != 200 || desc.RTCPMux || len(desc.Codecs) != 1 || desc.Codecs[0].Payload != int(tc.payload) || desc.Codecs[0].EncodingName != tc.codec {
				t.Fatalf("answer = %+v, err=%v", desc, err)
			}
			if tc.codec == "AMR" {
				params := voicehost.ParseSDPFmtpParameters(desc.Codecs[0].FMTP)
				if params["mode-set"] != tc.modeSet {
					t.Fatalf("mode-set changed: %v", params)
				}
				// Restricted-mode cases continue to real frames so the pre-fix
				// failure demonstrates encoder behavior, independently of SDP.
				if tc.modeSet == "" {
					if _, present := params["interleaving"]; present {
						t.Fatalf("answer declares unsupported interleaving: %v", params)
					}
				}
			}
			// A repeated offer must keep the negotiated format, not revert to PT 96.
			updated, err := controller.RoundTripInvite(ctx, request, nil)
			if err != nil || updated.StatusCode != 200 {
				t.Fatalf("re-INVITE = %+v %v", updated, err)
			}
			updateDesc, err := voicehost.ParseSDPMediaDescription(updated.Body)
			if err != nil || len(updateDesc.Codecs) != 1 || updateDesc.Codecs[0].Payload != int(tc.payload) {
				t.Fatalf("re-INVITE SDP = %+v %v", updateDesc, err)
			}
			for _, badFormat := range []string{"a=rtpmap:127 " + tc.codec + "/8000\r\n", tc.attributes + "a=fmtp:" + fmt.Sprint(tc.payload) + " interleaving=0\r\n"} {
				if tc.codec != "AMR" && strings.Contains(badFormat, "interleaving") {
					continue
				}
				bad := request
				payloads := tc.payloads
				if strings.Contains(badFormat, "rtpmap:127") {
					payloads = "127"
				}
				bad.Body = []byte(strings.ReplaceAll(string(incomingNegotiationSDP(payloads, badFormat)), "5000", "5990"))
				response, err := controller.RoundTripInvite(ctx, bad, nil)
				if err != nil || response.StatusCode != 488 {
					t.Fatalf("format-changing re-INVITE accepted: %+v %v", response, err)
				}
			}
			pcm := make([]byte, media.PCMFrameBytes)
			for i := 0; i < media.FrameSamples; i++ {
				binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(i*101-6500)))
			}
			// Feed and echo real encoded RTP, including any initial codec silence.
			voice := false
			lastMode, lastChange, atHighest := -1, uint16(0), 0
			for sequence := uint16(1); sequence <= 16; sequence++ {
				if ok, err := call.WritePCM(pcm, time.Now()); err != nil || !ok {
					t.Fatalf("PCM write: %t %v", ok, err)
				}
				if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				buffer := make([]byte, 2048)
				n, source, err := connection.ReadFrom(buffer)
				if err != nil {
					t.Fatal(err)
				}
				var packet rtp.Packet
				if err := packet.Unmarshal(buffer[:n]); err != nil {
					t.Fatal(err)
				}
				if packet.PayloadType != tc.payload {
					t.Fatalf("sent PT %d, want %d", packet.PayloadType, tc.payload)
				}
				if tc.modeSet != "" {
					// Inspect RFC 4867 BE ToC bits directly, not our own unpacker.
					mode := int(packet.Payload[0]&7)<<1 | int(packet.Payload[1]>>7)
					bits := map[int]int{0: 95, 2: 118, 7: 244}
					count, allowed := bits[mode]
					if !allowed || (tc.modeSet == "0" && mode != 0) || len(packet.Payload) != (10+count+7)/8 || packet.Payload[0]&8 != 0 {
						t.Fatalf("actual encoded frame violates mode-set/layout: mode=%d bytes=%d", mode, len(packet.Payload))
					}
					if lastMode == -1 && mode != 0 {
						t.Fatalf("initial encoded mode=%d, want 0", mode)
					}
					if lastMode != -1 && lastMode != mode {
						if lastChange != 0 && packet.SequenceNumber-lastChange < 2 {
							t.Fatal("mode changed before two frame-blocks")
						}
						order := map[int]int{0: 0, 2: 1, 7: 2}
						if order[mode]-order[lastMode] != 1 {
							t.Fatal("non-neighbor mode transition")
						}
						lastChange = packet.SequenceNumber
					}
					lastMode = mode
					if mode == 7 {
						atHighest++
					}
					if tc.modeSet != "0" {
						cmr := byte(7)
						if atHighest > 0 {
							cmr = 1
						} // Outside the set: must not change TX.
						packet.Payload[0] = packet.Payload[0]&15 | cmr<<4
					}
				}
				packet.SequenceNumber, packet.SSRC = sequence, 91
				if tc.codec == "AMR" {
					wrong := packet
					wrong.PayloadType = 96
					wire, err := wrong.Marshal()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := connection.WriteTo(wire, source); err != nil {
						t.Fatal(err)
					}
				}
				wire, err := packet.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := connection.WriteTo(wire, source); err != nil {
					t.Fatal(err)
				}
				select {
				case frame := <-call.PCM():
					voice = voice || (len(frame.Data) == media.PCMFrameBytes && !allZero(frame.Data))
				case <-ctx.Done():
					t.Fatalf("negotiated PT was not received: %v", ctx.Err())
				}
				if voice && (tc.modeSet != "0,2,7" || atHighest >= 4) {
					break
				}
			}
			if !voice {
				t.Fatal("no non-silent decoded PCM")
			}
			if tc.modeSet == "0,2,7" && atHighest < 4 {
				t.Fatal("CMR did not converge through allowed neighbor modes")
			}
			if tc.codec == "AMR" && call.bridge.Stats().RTPPacketsRejected == 0 {
				t.Fatal("unnegotiated PT 96 was accepted")
			}
			if ended, err := call.End(ctx); err != nil || !ended.Accepted || terminator.count() != 1 {
				t.Fatalf("end=%+v %v", ended, err)
			}
		})
	}
}

func TestIncomingNegotiationRejectsUnsupportedFormats(t *testing.T) {
	for _, tc := range []struct{ name, attributes string }{
		{"mux_only", "a=rtcp-mux-only\r\n"},
		{"octet_aligned", "a=fmtp:102 octet-align=1\r\n"},
		{"interleaving_zero", "a=fmtp:102 interleaving=0\r\n"},
		{"interleaving_positive", "a=fmtp:102 interleaving=2\r\n"},
		{"invalid_mode_set", "a=fmtp:102 mode-set=8\r\n"},
		{"invalid_mode_period", "a=fmtp:102 mode-change-period=3\r\n"},
		{"unsupported_ptime", "a=ptime:40\r\n"},
		{"insufficient_maxptime", "a=maxptime:10\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := openStackPair(t)
			controller := newTestIncomingController(t, client, &incomingTerminator{})
			request := incomingInvite(tc.name, 5000, 5001)
			request.Body = incomingNegotiationSDP("102", "a=rtpmap:102 AMR/8000\r\n"+tc.attributes)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			response, err := controller.RoundTripInvite(ctx, request, nil)
			if err != nil || response.StatusCode != 488 {
				t.Fatalf("unsupported offer = %+v %v", response, err)
			}
			if _, pending := controller.Pending(); pending {
				t.Fatal("unsupported offer acquired call ownership")
			}
		})
	}
}

func incomingNegotiationSDP(payloads, attributes string) []byte {
	return []byte("v=0\r\no=test 1 1 IN IP4 10.0.0.2\r\ns=test\r\nc=IN IP4 10.0.0.2\r\nt=0 0\r\nm=audio 5000 RTP/AVP " + payloads + "\r\na=sendrecv\r\n" + attributes)
}
