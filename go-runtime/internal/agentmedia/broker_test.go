package agentmedia

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func TestBrokerPairsOneReservedAgentAndPreservesPCMFrames(t *testing.T) {
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(_ context.Context, agentID string) (string, error) {
		if agentID != "agent-1" {
			return "", ErrReservationNotFound
		}
		return mediaTestToken, nil
	}), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Reserve(Reservation{
		AgentID: "agent-1", ProcessGeneration: "generation-1", SessionID: "session-1",
		MediaToken: mediaTestToken, ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	factory := &endpointFactory{}
	manager, err := NewManager(Config{
		Context: context.Background(), ServerURL: strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/agent/ws",
		ServerToken: mediaTestToken, AgentID: "agent-1", ProcessGeneration: "generation-1",
		HTTPClient: server.Client(), Endpoints: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	request := agentlink.ModemMediaRequest{
		OperationID: "prepare-1", AttachmentID: "attachment-1", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: agentlink.ModemMediaPrepare,
		SessionID: "session-1", MediaToken: mediaTestToken,
	}
	if result := manager.ExecuteModemMedia(context.Background(), request); result.Failure != nil {
		t.Fatalf("prepare result=%+v", result)
	}
	acquireContext, cancelAcquire := context.WithTimeout(context.Background(), time.Second)
	peer, err := broker.Acquire(acquireContext, "session-1")
	cancelAcquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Acquire(context.Background(), "session-1"); err != ErrMediaAlreadyClaimed {
		t.Fatalf("second acquire error=%v", err)
	}

	factory.mu.Lock()
	hardware := factory.hardware
	factory.mu.Unlock()
	downlink := make([]byte, pcmFrameBytes)
	for index := range downlink {
		downlink[index] = byte(index)
	}
	writeDone := make(chan error, 1)
	go func() { _, err := hardware.Write(downlink); writeDone <- err }()
	readContext, cancelRead := context.WithTimeout(context.Background(), time.Second)
	frame, err := peer.Read(readContext)
	cancelRead()
	if err != nil || string(frame) != string(downlink) {
		t.Fatalf("downlink bytes=%d err=%v", len(frame), err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}

	uplink := make([]byte, serialPCMWriteBytes)
	for offset := 0; offset < len(uplink); offset += pcmFrameBytes {
		frame := uplink[offset : offset+pcmFrameBytes]
		for index := range frame {
			frame[index] = byte(offset/pcmFrameBytes + 1)
		}
		if err := peer.Write(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
	}
	received := make([]byte, len(uplink))
	if _, err := io.ReadFull(hardware, received); err != nil || string(received) != string(uplink) {
		t.Fatalf("uplink bytes=%d err=%v", len(received), err)
	}

	broker.Revoke("session-1")
	select {
	case <-peer.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation did not close Agent media")
	}
}
