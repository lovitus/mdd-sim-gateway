// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
)

func TestOutboundAgentInviteAckByeOverUserspaceStack(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	packetConn, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5060")
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()
	_ = packetConn.SetDeadline(time.Now().Add(8 * time.Second))

	requests := make(chan voiceclient.SIPIncomingRequest, 5)
	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 65535)
		for count := 0; count < 5; count++ {
			size, source, err := packetConn.ReadFrom(buffer)
			if err != nil {
				serverDone <- err
				return
			}
			request, err := voiceclient.ParseSIPRequest(buffer[:size])
			if err != nil {
				serverDone <- err
				return
			}
			requests <- request
			if request.Method == "ACK" {
				continue
			}
			headers, body := fakePCSCFResponse(request)
			wire, err := voiceclient.BuildSIPResponseWire(request, 200, "OK", headers, body)
			if err == nil {
				_, err = packetConn.WriteTo(wire, source)
			}
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	registrar, err := NewRegistrar(clientStack, runtimehost.WireIMSRegistrar{
		Network: "udp4", ServerAddr: "10.0.0.2:5060", ContactHost: "10.0.0.1",
		Expires: 600, DisableRefresh: true, DisableKeepalive: true, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := registrar.RegisterIMS(context.Background(), runtimehost.IMSRegistrationConfig{
		DeviceID: "device-voice", TraceID: "trace-voice",
		Profile: identity.Profile{IMSI: "001010123456789", MCC: "001", MNC: "01"},
		Prepared: &identity.PreparedSession{IMSIdentity: identity.IMSIdentityResolution{
			IMPI: "user@ims.test", IMPU: "sip:user@ims.test", Domain: "ims.test",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewOutboundAgent(registration)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := registration.Close(callCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	close(requests)
	var methods []string
	for request := range requests {
		methods = append(methods, request.Method)
	}
	want := []string{"REGISTER", "INVITE", "ACK", "BYE", "REGISTER"}
	if fmt.Sprint(methods) != fmt.Sprint(want) {
		t.Fatalf("SIP methods = %v, want %v", methods, want)
	}
}

func TestNewOutboundAgentDoesNotTreatRegistrationWithoutVoiceAsReady(t *testing.T) {
	agent, err := NewOutboundAgent(runtimehost.IMSRegistrationResult{Registered: true})
	if agent != nil || !errors.Is(err, ErrVoiceNotReady) {
		t.Fatalf("NewOutboundAgent() = %v, %v", agent, err)
	}
}

func fakePCSCFResponse(request voiceclient.SIPIncomingRequest) (map[string]string, []byte) {
	switch request.Method {
	case "REGISTER":
		return map[string]string{"P-Associated-URI": "<sip:user@ims.test>"}, nil
	case "INVITE":
		return map[string]string{
			"To":           firstHeader(request.Headers, "To") + ";tag=pcscf",
			"Contact":      "<sip:callee@10.0.0.2:5060>",
			"Content-Type": "application/sdp",
		}, []byte("v=0\r\no=- 2 2 IN IP4 10.0.0.2\r\ns=-\r\nc=IN IP4 10.0.0.2\r\nt=0 0\r\nm=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n")
	case "BYE":
		return nil, nil
	default:
		return map[string]string{"Warning": strings.TrimSpace(request.Method)}, nil
	}
}
