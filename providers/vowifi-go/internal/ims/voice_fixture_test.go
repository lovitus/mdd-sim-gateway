// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

func registeredVoiceFixture(
	t *testing.T,
	clientStack, serverStack *usernet.Stack,
	byeStatuses []int,
	answerAttributes ...string,
) (*voicehost.IMSOutboundAgent, runtimehost.IMSRegistrationResult, <-chan voiceclient.SIPIncomingRequest, <-chan error) {
	t.Helper()
	packetConn, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5060")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packetConn.Close() })
	_ = packetConn.SetDeadline(time.Now().Add(15 * time.Second))
	requestCount := 4 + len(byeStatuses)
	requests := make(chan voiceclient.SIPIncomingRequest, requestCount)
	serverDone := make(chan error, 1)
	go func() {
		defer close(requests)
		buffer := make([]byte, 65535)
		seen := make(map[string]struct{}, requestCount)
		byeIndex := 0
		for len(seen) < requestCount {
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
			transaction := request.Method + "|" + firstHeader(request.Headers, "Call-ID") + "|" +
				firstHeader(request.Headers, "CSeq") + "|" + firstHeader(request.Headers, "Expires")
			_, duplicate := seen[transaction]
			if !duplicate {
				seen[transaction] = struct{}{}
				requests <- request
			}
			if request.Method == "ACK" {
				continue
			}
			status := 200
			reason := "OK"
			if request.Method == "BYE" {
				if !duplicate && byeIndex >= len(byeStatuses) {
					serverDone <- fmt.Errorf("unexpected BYE %d", byeIndex+1)
					return
				}
				index := byeIndex
				if duplicate {
					index--
				}
				if index < 0 || index >= len(byeStatuses) {
					serverDone <- fmt.Errorf("invalid duplicate BYE transaction")
					return
				}
				status = byeStatuses[index]
				if !duplicate {
					byeIndex++
				}
				if status != 200 {
					reason = "Rejected"
				}
			}
			headers, body := fakePCSCFResponse(request)
			if request.Method == "INVITE" {
				body = append(body, []byte(strings.Join(answerAttributes, ""))...)
			}
			wire, err := voiceclient.BuildSIPResponseWire(request, status, reason, headers, body)
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
		Expires: 600, DisableRefresh: true, DisableKeepalive: true, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := registrar.RegisterIMS(context.Background(), runtimehost.IMSRegistrationConfig{
		DeviceID: "device-media", TraceID: "trace-media",
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
	return agent, registration, requests, serverDone
}

func finishVoiceFixture(
	t *testing.T,
	registration runtimehost.IMSRegistrationResult,
	requests <-chan voiceclient.SIPIncomingRequest,
	serverDone <-chan error,
	want []string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := registration.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	var methods []string
	for request := range requests {
		methods = append(methods, request.Method)
	}
	if fmt.Sprint(methods) != fmt.Sprint(want) {
		t.Fatalf("SIP methods = %v, want %v", methods, want)
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
