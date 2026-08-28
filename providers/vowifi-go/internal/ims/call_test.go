// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
)

func TestOutboundAgentInviteAckByeOverUserspaceStack(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	agent, registration, requests, serverDone := registeredVoiceFixture(t, clientStack, serverStack, []int{200})
	offer := []byte("v=0\r\no=- 1 1 IN IP4 10.0.0.1\r\ns=-\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n")
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, err := agent.StartOutboundCall(callCtx, voicehost.OutboundCallRequest{
		DeviceID: "device-voice", CallID: "call-1", Callee: "+100", RawSDP: offer,
	})
	if err != nil || !started.Accepted || started.StatusCode != 200 {
		t.Fatalf("StartOutboundCall() = %+v, %v", started, err)
	}
	ended, err := agent.EndVoiceCallWithResult(callCtx, voicehost.DialogInfo{
		DeviceID: "device-voice", CallID: "call-1",
	})
	if err != nil || !ended.Accepted || ended.StatusCode != 200 {
		t.Fatalf("EndVoiceCallWithResult() = %+v, %v", ended, err)
	}
	finishVoiceFixture(t, registration, requests, serverDone,
		[]string{"REGISTER", "INVITE", "ACK", "BYE", "REGISTER"})
}

func TestNewOutboundAgentDoesNotTreatRegistrationWithoutVoiceAsReady(t *testing.T) {
	agent, err := NewOutboundAgent(runtimehost.IMSRegistrationResult{Registered: true})
	if agent != nil || !errors.Is(err, ErrVoiceNotReady) {
		t.Fatalf("NewOutboundAgent() = %v, %v", agent, err)
	}
}
