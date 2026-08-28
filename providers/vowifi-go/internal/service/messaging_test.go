// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"testing"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/messaging"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type captureSMSTransport struct{ requests []messaging.SMSSendRequest }

func (transport *captureSMSTransport) SendSMSPart(_ context.Context, request messaging.SMSSendRequest) (messaging.SMSSendResult, error) {
	transport.requests = append(transport.requests, request)
	return messaging.SMSSendResult{State: "accepted", SIPCode: 202}, nil
}

func TestUpstreamRuntimeUsesRegisteredSMSTransport(t *testing.T) {
	transport := &captureSMSTransport{}
	runtime := &upstreamRuntime{
		deviceID: "device-1", imsi: "234100000000001",
		registration: runtimehost.IMSRegistrationResult{Registered: true, SMSTransport: transport},
	}
	request := vowifiipc.SendMessageRequest{
		OperationID: "send-1", MessageID: "message-1", Recipient: "+441234567890", Body: "hello",
	}
	if err := runtime.SendMessage(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("SMS transport requests=%d", len(transport.requests))
	}
	got := transport.requests[0]
	if got.DeviceID != runtime.deviceID || got.IMSI != runtime.imsi || got.Peer != request.Recipient || got.Part.Text != request.Body {
		t.Fatalf("SMS request=%+v", got)
	}
}

func TestUpstreamRuntimeRejectsMissingCurrentMessagingTransport(t *testing.T) {
	runtime := &upstreamRuntime{registration: runtimehost.IMSRegistrationResult{Registered: true}}
	err := runtime.SendMessage(context.Background(), vowifiipc.SendMessageRequest{
		OperationID: "send-1", MessageID: "message-1", Recipient: "+441234567890", Body: "hello",
	})
	if operationCode(err) != "messaging_transport_unavailable" {
		t.Fatalf("SendMessage() err=%v", err)
	}
}
