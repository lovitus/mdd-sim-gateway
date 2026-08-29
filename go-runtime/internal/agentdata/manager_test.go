package agentdata

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const managerTestToken = "0123456789abcdef0123456789abcdef"

type fakeBackend struct {
	mu    sync.Mutex
	peers []net.Conn
	stops int
}

func (backend *fakeBackend) PrepareData(context.Context, Target, string) (string, error) {
	return "profile-a", nil
}
func (backend *fakeBackend) DialData(_ context.Context, _ Target, _, _ string) (net.Conn, error) {
	left, right := net.Pipe()
	backend.mu.Lock()
	backend.peers = append(backend.peers, right)
	backend.mu.Unlock()
	return left, nil
}
func (backend *fakeBackend) StopData(context.Context, Target) error {
	backend.mu.Lock()
	backend.stops++
	backend.mu.Unlock()
	return nil
}
func (backend *fakeBackend) peer(index int) net.Conn {
	deadline := time.Now().Add(time.Second)
	for {
		backend.mu.Lock()
		if len(backend.peers) > index {
			value := backend.peers[index]
			backend.mu.Unlock()
			return value
		}
		backend.mu.Unlock()
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerBridgesTCPAndUDPAndStopsBackend(t *testing.T) {
	tokens := agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return managerTestToken, nil })
	broker, err := NewBroker(tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{}
	manager, err := NewManager(Config{Context: ctx, ServerURL: server.URL, ServerToken: managerTestToken,
		AgentID: "agent-a", ProcessGeneration: "process-a", HTTPClient: &http.Client{Timeout: 5 * time.Second}, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	expires := time.Now().UTC().Add(time.Hour)
	prepare := agentlink.ModemDataRequest{OperationID: "prepare-a", AttachmentID: "attachment-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: agentlink.ModemDataPrepare, SessionID: "session-a", ExpiresAt: expires, MaxBytes: 1 << 20}
	if result := manager.ExecuteModemData(context.Background(), prepare); result.Failure != nil || result.State != "ready" || result.Profile != "profile-a" {
		t.Fatalf("prepare=%+v", result)
	}
	for index, network := range []string{"tcp", "udp"} {
		streamID := "stream-" + network
		if err := broker.Reserve(Reservation{AgentID: "agent-a", ProcessGeneration: "process-a", SessionID: "session-a", StreamID: streamID,
			StreamToken: managerTestToken, Network: network, ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
		open := agentlink.ModemDataRequest{OperationID: "open-" + network, AttachmentID: "attachment-a", EquipmentID: "862547055201716",
			CardID: "8985200000000000001", Action: agentlink.ModemDataOpen, SessionID: "session-a", StreamID: streamID,
			StreamToken: managerTestToken, Network: network, Address: "192.0.2.1:53", ExpiresAt: expires, MaxBytes: 1 << 20}
		if result := manager.ExecuteModemData(context.Background(), open); result.Failure != nil || result.State != "open" {
			t.Fatalf("open %s=%+v", network, result)
		}
		acquireCtx, acquireCancel := context.WithTimeout(context.Background(), time.Second)
		core, err := broker.Acquire(acquireCtx, streamID)
		acquireCancel()
		if err != nil {
			t.Fatal(err)
		}
		remote := backend.peer(index)
		if remote == nil {
			t.Fatal("backend peer was not opened")
		}
		payload := []byte("request-" + network)
		if _, err := core.Write(payload); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(remote, got); err != nil {
			t.Fatal(err)
		}
		if string(got) != string(payload) {
			t.Fatalf("remote got %q", got)
		}
		reply := []byte("reply-" + network)
		go func() { _, _ = remote.Write(reply) }()
		got = make([]byte, len(reply))
		if _, err := io.ReadFull(core, got); err != nil {
			t.Fatal(err)
		}
		if string(got) != string(reply) {
			t.Fatalf("core got %q", got)
		}
		_ = core.Close()
		_ = remote.Close()
	}
	stop := agentlink.ModemDataRequest{OperationID: "stop-a", AttachmentID: "attachment-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: agentlink.ModemDataStop, SessionID: "session-a"}
	if result := manager.ExecuteModemData(context.Background(), stop); result.Failure != nil || result.State != "stopped" {
		t.Fatalf("stop=%+v", result)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	stops := backend.stops
	backend.mu.Unlock()
	if stops != 1 {
		t.Fatalf("backend stop count=%d, want exactly one", stops)
	}
}
