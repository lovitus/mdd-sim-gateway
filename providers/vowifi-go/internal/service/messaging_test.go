// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/messaging"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type captureSMSTransport struct{ requests []messaging.SMSSendRequest }

type captureMessageSink struct{ events []providermessages.Event }

func (sink *captureMessageSink) Publish(event providermessages.Event) error {
	sink.events = append(sink.events, event)
	return nil
}

type failingMessageSink struct{}

func (failingMessageSink) Publish(providermessages.Event) error { return context.DeadlineExceeded }

func (transport *captureSMSTransport) SendSMSPart(_ context.Context, request messaging.SMSSendRequest) (messaging.SMSSendResult, error) {
	transport.requests = append(transport.requests, request)
	return messaging.SMSSendResult{State: "accepted", SIPCode: 202, CallID: "carrier-call", RPMR: 7}, nil
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
	if got.DeviceID != runtime.deviceID || got.IMSI != runtime.imsi || got.Peer != request.Recipient || got.Part.Text != request.Body || got.MessageID != request.MessageID {
		t.Fatalf("SMS request=%+v", got)
	}
}

func TestInboundMessageBridgeParsesAndQueuesBusinessEvent(t *testing.T) {
	sink := &captureMessageSink{}
	identity := func(eventID string, kind providermessages.Kind) providermessages.Event {
		return providermessages.Event{
			SchemaVersion: providermessages.SchemaVersion, EventID: "generation-1:" + eventID,
			LineID: "line-1", ProviderID: "provider-1", ProcessGeneration: "generation-1",
			Kind: kind, ObservedAt: time.Now(),
		}
	}
	tracker := newMessageTracker(sink, identity)
	service := messaging.NewService("device-1", "234100000000001", tracker, nil)
	inbound := &inboundMessaging{service: service, tracker: tracker, sink: sink}
	result, err := inbound.handle(context.Background(), voicehost.IMSMessageRequest{
		FromURI: "sip:+44123@example", ToURI: "sip:+44999@example", CallID: "incoming-call", CSeq: 7,
		ContentType: "text/plain", Body: []byte("hello"),
	})
	if err != nil || result.StatusCode != 200 {
		t.Fatalf("handle() result=%+v err=%v", result, err)
	}
	if len(sink.events) != 1 || sink.events[0].Kind != providermessages.KindReceived ||
		sink.events[0].Sender != "sip:+44123@example" || sink.events[0].Body != "hello" || sink.events[0].EventID != "generation-1:received:incoming-call:7" {
		t.Fatalf("events=%+v", sink.events)
	}
}

func TestInboundMessageBridgeReturnsServerFailureWhenDurableQueueFails(t *testing.T) {
	identity := func(eventID string, kind providermessages.Kind) providermessages.Event {
		return providermessages.Event{
			SchemaVersion: providermessages.SchemaVersion, EventID: "generation-1:" + eventID,
			LineID: "line-1", ProviderID: "provider-1", ProcessGeneration: "generation-1",
			Kind: kind, ObservedAt: time.Now(),
		}
	}
	tracker := newMessageTracker(failingMessageSink{}, identity)
	service := messaging.NewService("device-1", "234100000000001", tracker, nil)
	inbound := &inboundMessaging{service: service, tracker: tracker, sink: failingMessageSink{}}
	result, err := inbound.handle(context.Background(), voicehost.IMSMessageRequest{
		FromURI: "sip:+44123@example", ToURI: "sip:+44999@example", CallID: "incoming-call", CSeq: 7,
		ContentType: "text/plain", Body: []byte("hello"),
	})
	if err == nil || result.StatusCode != 500 {
		t.Fatalf("handle() result=%+v err=%v", result, err)
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

func TestOutboundMessageReportsDurableCorrelationFailureWithoutResending(t *testing.T) {
	identity := func(eventID string, kind providermessages.Kind) providermessages.Event {
		return providermessages.Event{
			SchemaVersion: providermessages.SchemaVersion, EventID: "generation-1:" + eventID,
			LineID: "line-1", ProviderID: "provider-1", ProcessGeneration: "generation-1",
			Kind: kind, ObservedAt: time.Now(),
		}
	}
	tracker := newMessageTracker(failingMessageSink{}, identity)
	messagingService := messaging.NewService("device-1", "234100000000001", tracker, nil)
	transport := &captureSMSTransport{}
	runtime := &upstreamRuntime{
		deviceID: "device-1", imsi: "234100000000001", messaging: messagingService, tracker: tracker,
		registration: runtimehost.IMSRegistrationResult{Registered: true, SMSTransport: transport},
	}
	err := runtime.SendMessage(context.Background(), vowifiipc.SendMessageRequest{
		OperationID: "send-1", MessageID: "message-1", Recipient: "+441234567890", Body: "hello",
	})
	if operationCode(err) != "message_status_persist_failed" || len(transport.requests) != 1 {
		t.Fatalf("SendMessage() err=%v requests=%d", err, len(transport.requests))
	}
}
