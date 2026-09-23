// SPDX-License-Identifier: AGPL-3.0-only
package ims

import (
	"errors"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"testing"
)

func TestPR8MediaFailureWithRejectedByeReturnsCleanupHandle(t *testing.T) {
	client, server := openStackPair(t)
	agent, registration, requests, done := registeredVoiceFixture(t, client, server, []int{503, 200}, "a=ptime:30\r\n")
	call, result, err := StartMediaCall(t.Context(), agent, client, MediaCallConfig{LocalRTP: "10.0.0.1:0", LocalRTCP: "10.0.0.1:0", Codec: media.CodecPCMU, BufferMS: 500}, voicehost.OutboundCallRequest{DeviceID: "device-media", CallID: "bad-media-and-bye", Callee: "+100"})
	if call == nil || !result.Accepted || !errors.Is(err, ErrMediaNegotiation) {
		t.Fatalf("media failure discarded unconfirmed dialog: call=%v result=%+v err=%v", call, result, err)
	}
	if end, err := call.End(t.Context()); err != nil || !end.Accepted {
		t.Fatal(end, err)
	}
	finishVoiceFixture(t, registration, requests, done, []string{"REGISTER", "INVITE", "ACK", "BYE", "BYE", "REGISTER"})
}
