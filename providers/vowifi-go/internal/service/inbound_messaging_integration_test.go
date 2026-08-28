// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/messaging"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

type messagingPacketSession struct {
	inbound chan []byte
	peer    *messagingPacketSession
	closed  chan struct{}
	once    sync.Once
}

func messagingPacketPair() (*messagingPacketSession, *messagingPacketSession) {
	first := &messagingPacketSession{inbound: make(chan []byte, 32), closed: make(chan struct{})}
	second := &messagingPacketSession{inbound: make(chan []byte, 32), closed: make(chan struct{})}
	first.peer, second.peer = second, first
	return first, second
}

func (session *messagingPacketSession) Send(ctx context.Context, packet []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.peer.closed:
		return usernet.ErrClosed
	case session.peer.inbound <- append([]byte(nil), packet...):
		return nil
	}
}

func (session *messagingPacketSession) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-session.closed:
		return nil, usernet.ErrClosed
	case packet := <-session.inbound:
		return append([]byte(nil), packet...), nil
	}
}

func (session *messagingPacketSession) Close(context.Context) error {
	session.once.Do(func() { close(session.closed) })
	return nil
}

func TestInboundSIPMessageSharesRegisteredUserspaceFlow(t *testing.T) {
	clientPackets, serverPackets := messagingPacketPair()
	clientStack, err := usernet.Open(context.Background(), clientPackets, usernet.Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	serverStack, err := usernet.Open(context.Background(), serverPackets, usernet.Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = clientStack.Close(ctx)
		_ = serverStack.Close(ctx)
	})

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
	inbound, err := newInboundMessaging(service, tracker, sink)
	if err != nil {
		t.Fatal(err)
	}
	flow := &voiceclient.WireSIPFlow{
		Network: "udp4", ServerAddr: "10.0.0.2:5060", LocalAddr: "10.0.0.1:5060",
		Timeout: 2 * time.Second, DialContext: clientStack.DialContext, DialContextLocal: clientStack.DialContextLocal,
		IncomingHandler: inbound,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inbound.Close(ctx)
		_ = flow.Close()
	})
	server, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5060")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 65535)
		count, source, err := server.ReadFrom(buffer)
		if err != nil {
			serverDone <- err
			return
		}
		request, err := voiceclient.ParseSIPRequest(buffer[:count])
		if err != nil {
			serverDone <- err
			return
		}
		response, err := voiceclient.BuildSIPResponseWire(request, 200, "OK", nil, nil)
		if err == nil {
			_, err = server.WriteTo(response, source)
		}
		if err != nil {
			serverDone <- err
			return
		}
		body := "hello over SWu"
		wire := fmt.Sprintf("MESSAGE sip:user@10.0.0.1 SIP/2.0\r\nVia: SIP/2.0/UDP 10.0.0.2:5060;branch=z9hG4bK-inbound\r\nFrom: <sip:+44123@example>;tag=remote\r\nTo: <sip:user@example>\r\nCall-ID: inbound-swu-1\r\nCSeq: 1 MESSAGE\r\nMax-Forwards: 70\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
		if _, err = server.WriteTo([]byte(wire), source); err != nil {
			serverDone <- err
			return
		}
		gotMessageResponse := false
		for attempts := 0; attempts < 4; attempts++ {
			count, duplicateSource, readErr := server.ReadFrom(buffer)
			if readErr != nil {
				err = readErr
				break
			}
			if count >= len("SIP/2.0 200") && string(buffer[:len("SIP/2.0 200")]) == "SIP/2.0 200" {
				gotMessageResponse = true
				err = nil
				break
			}
			duplicate, parseErr := voiceclient.ParseSIPRequest(buffer[:count])
			if parseErr != nil || duplicate.Method != "OPTIONS" {
				err = fmt.Errorf("unexpected response %q", buffer[:count])
				break
			}
			duplicateResponse, buildErr := voiceclient.BuildSIPResponseWire(duplicate, 200, "OK", nil, nil)
			if buildErr != nil {
				err = buildErr
				break
			}
			_, err = server.WriteTo(duplicateResponse, duplicateSource)
		}
		if err == nil && !gotMessageResponse {
			err = errors.New("inbound MESSAGE response not received")
		}
		serverDone <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = flow.RoundTripRequest(ctx, voiceclient.SIPRequestMessage{
		Method: "OPTIONS", URI: "sip:ims.example", Headers: map[string]string{
			"From": "<sip:user@example>;tag=local", "To": "<sip:ims.example>",
			"Call-ID": "establish-flow", "CSeq": "1 OPTIONS", "Max-Forwards": "70",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := inbound.Start(flow); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if len(sink.events) != 1 || sink.events[0].Body != "hello over SWu" || sink.events[0].Kind != providermessages.KindReceived {
		t.Fatalf("events=%+v", sink.events)
	}
}
