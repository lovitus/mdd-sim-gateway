package agentmedia

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

const mediaTestToken = "0123456789abcdef0123456789abcdef"

type endpointFactory struct {
	mu       sync.Mutex
	opens    int
	target   agentmodem.MediaTarget
	hardware net.Conn
}

func (factory *endpointFactory) OpenVoicePCM(_ context.Context, target agentmodem.MediaTarget) (io.ReadWriteCloser, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	agent, hardware := net.Pipe()
	factory.opens++
	factory.target = target
	factory.hardware = hardware
	return agent, nil
}

func TestManagerBridgesExactPCMFramesOverOutboundWebSocket(t *testing.T) {
	attached := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/agent/media/ws" || request.Header.Get("Authorization") != "Bearer "+mediaTestToken ||
			request.Header.Get("X-MDD-Agent-ID") != "agent-1" || request.Header.Get("X-MDD-Agent-Generation") != "generation-1" ||
			request.Header.Get("X-MDD-Media-Session") != "session-1" || request.Header.Get("X-MDD-Media-Token") != mediaTestToken {
			http.Error(response, "wrong media identity", http.StatusUnauthorized)
			return
		}
		socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		payload, _ := json.Marshal(map[string]any{"type": "agent.media.ready", "version": 1, "session_id": "session-1"})
		if socket.Write(request.Context(), websocket.MessageText, payload) != nil {
			socket.CloseNow()
			return
		}
		attached <- socket
		<-request.Context().Done()
	}))
	defer server.Close()

	factory := &endpointFactory{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := NewManager(Config{
		Context: ctx, ServerURL: strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/agent/ws",
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
	if result := manager.ExecuteModemMedia(context.Background(), request); result.Failure != nil || result.State != "ready" {
		t.Fatalf("prepare result=%+v", result)
	}
	socket := <-attached
	defer socket.CloseNow()

	factory.mu.Lock()
	hardware := factory.hardware
	openedTarget := factory.target
	opens := factory.opens
	factory.mu.Unlock()
	if opens != 1 || openedTarget.AttachmentID != request.AttachmentID || openedTarget.EquipmentID != request.EquipmentID || openedTarget.CardID != request.CardID {
		t.Fatalf("opens=%d target=%+v", opens, openedTarget)
	}

	downlink := make([]byte, pcmFrameBytes)
	for index := range downlink {
		downlink[index] = byte(index)
	}
	writeDone := make(chan error, 1)
	go func() { _, err := hardware.Write(downlink); writeDone <- err }()
	messageType, payload, err := socket.Read(context.Background())
	if err != nil || messageType != websocket.MessageBinary || string(payload) != string(downlink) {
		t.Fatalf("downlink type=%v bytes=%d err=%v", messageType, len(payload), err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}

	uplink := make([]byte, pcmWriteBytes)
	for frame := 0; frame < pcmWriteBytes/pcmFrameBytes; frame++ {
		payload := uplink[frame*pcmFrameBytes : (frame+1)*pcmFrameBytes]
		for index := range payload {
			payload[index] = byte(frame + 1)
		}
		if err := socket.Write(context.Background(), websocket.MessageBinary, payload); err != nil {
			t.Fatal(err)
		}
	}
	received := make([]byte, pcmWriteBytes)
	if _, err := io.ReadFull(hardware, received); err != nil || string(received) != string(uplink) {
		t.Fatalf("uplink bytes=%d err=%v", len(received), err)
	}

	// Retrying the exact prepare is idempotent and must not reopen the modem.
	request.OperationID = "prepare-1-retry"
	if result := manager.ExecuteModemMedia(context.Background(), request); result.Failure != nil || result.State != "ready" {
		t.Fatalf("retry result=%+v", result)
	}
	factory.mu.Lock()
	opens = factory.opens
	factory.mu.Unlock()
	if opens != 1 {
		t.Fatalf("exact retry opened %d endpoints", opens)
	}

	stop := request
	stop.OperationID = "stop-1"
	stop.Action = agentlink.ModemMediaStop
	stop.MediaToken = ""
	stopContext, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if result := manager.ExecuteModemMedia(stopContext, stop); result.Failure != nil || result.State != "stopped" {
		t.Fatalf("stop result=%+v", result)
	}
	_ = hardware.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := hardware.Read(make([]byte, 1)); err == nil {
		t.Fatal("hardware endpoint remained open after stop")
	}
}
