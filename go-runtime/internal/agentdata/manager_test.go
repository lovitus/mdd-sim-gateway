package agentdata

import (
	"context"
	"errors"
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
	mu           sync.Mutex
	peers        []net.Conn
	stops        int
	stopFailures int
}

type passCoordinator struct{}
type passAdmission struct{}

func (passCoordinator) DoAuxiliary(ctx context.Context, _ string, callback func(context.Context) error) error {
	return callback(ctx)
}

func (passAdmission) ResolveDataProfile(_ context.Context, _ Target, requested, _, _ string) (Profile, error) {
	if requested == "" {
		requested = "profile-a"
	}
	return Profile{Name: requested, AllowRoaming: true}, nil
}
func (passAdmission) ValidateDataTarget(context.Context, Target) error { return nil }
func (passAdmission) DataPrepared(Target, string, string)              {}
func (passAdmission) DataCleanup(Target, string, string)               {}
func (passAdmission) DataReleased(Target, string)                      {}

func (backend *fakeBackend) PrepareData(_ context.Context, _ Target, profile Profile) (string, error) {
	if profile.Name == "" {
		return "profile-a", nil
	}
	return profile.Name, nil
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
	defer backend.mu.Unlock()
	backend.stops++
	if backend.stopFailures > 0 {
		backend.stopFailures--
		return errors.New("injected cleanup failure")
	}
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
		AgentID: "agent-a", ProcessGeneration: "process-a", HTTPClient: &http.Client{Timeout: 5 * time.Second},
		Backend: backend, Coordinator: passCoordinator{}, Admission: passAdmission{}})
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

func TestAuxiliaryIsRejectedUntilExactDataSessionStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := NewManager(Config{
		Context: ctx, ServerURL: "http://127.0.0.1:1", ServerToken: managerTestToken,
		AgentID: "agent-a", ProcessGeneration: "process-a", HTTPClient: http.DefaultClient,
		Backend: &fakeBackend{}, Coordinator: passCoordinator{}, Admission: passAdmission{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	expires := time.Now().UTC().Add(time.Hour)
	prepare := agentlink.ModemDataRequest{
		OperationID: "prepare-a", AttachmentID: "attachment-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: agentlink.ModemDataPrepare,
		SessionID: "session-a", ExpiresAt: expires, MaxBytes: 1 << 20,
	}
	if response := manager.ExecuteModemData(context.Background(), prepare); response.Failure != nil {
		t.Fatalf("prepare=%+v", response)
	}
	called := false
	if err := manager.DoAuxiliary(context.Background(), prepare.EquipmentID, func(context.Context) error {
		called = true
		return nil
	}); !errors.Is(err, ErrSessionActive) || called {
		t.Fatalf("active data auxiliary err=%v called=%t", err, called)
	}
	stop := agentlink.ModemDataRequest{
		OperationID: "stop-a", AttachmentID: prepare.AttachmentID, EquipmentID: prepare.EquipmentID,
		CardID: prepare.CardID, Action: agentlink.ModemDataStop, SessionID: prepare.SessionID,
	}
	if response := manager.ExecuteModemData(context.Background(), stop); response.Failure != nil {
		t.Fatalf("stop=%+v", response)
	}
	if err := manager.DoAuxiliary(context.Background(), prepare.EquipmentID, func(context.Context) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("idle auxiliary err=%v called=%t", err, called)
	}
}

func TestFailedDataCleanupRetainsAdmissionAndRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{stopFailures: 1}
	manager, err := NewManager(Config{
		Context: ctx, ServerURL: "http://127.0.0.1:1", ServerToken: managerTestToken,
		AgentID: "agent-a", ProcessGeneration: "process-a", HTTPClient: http.DefaultClient,
		Backend: backend, Coordinator: passCoordinator{}, Admission: passAdmission{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	request := agentlink.ModemDataRequest{
		OperationID: "prepare-retry", AttachmentID: "attachment-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: agentlink.ModemDataPrepare,
		SessionID: "session-retry", ExpiresAt: time.Now().Add(time.Hour), MaxBytes: 1 << 20,
	}
	if response := manager.ExecuteModemData(context.Background(), request); response.Failure != nil {
		t.Fatalf("prepare=%+v", response)
	}
	stop := agentlink.ModemDataRequest{
		OperationID: "stop-retry", AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, Action: agentlink.ModemDataStop, SessionID: request.SessionID,
	}
	if response := manager.ExecuteModemData(context.Background(), stop); response.Failure == nil {
		t.Fatal("injected first cleanup failure was hidden")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := manager.DoAuxiliary(context.Background(), request.EquipmentID, func(context.Context) error { return nil })
		if err == nil {
			break
		}
		if !errors.Is(err, ErrSessionActive) || time.Now().After(deadline) {
			t.Fatalf("cleanup admission did not recover: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	backend.mu.Lock()
	stops := backend.stops
	backend.mu.Unlock()
	if stops != 2 {
		t.Fatalf("backend stop attempts=%d, want failure plus one retry", stops)
	}
}

func TestRenewExtendsOneExactDataSessionWithoutCreatingAnotherOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{}
	manager, err := NewManager(Config{Context: ctx, ServerURL: "http://127.0.0.1:1", ServerToken: managerTestToken,
		AgentID: "agent-a", ProcessGeneration: "process-a", HTTPClient: http.DefaultClient, Backend: backend,
		Coordinator: passCoordinator{}, Admission: passAdmission{}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	original := time.Now().UTC().Add(80 * time.Millisecond)
	prepare := agentlink.ModemDataRequest{OperationID: "prepare-renew", AttachmentID: "attachment-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: agentlink.ModemDataPrepare, SessionID: "session-renew", Purpose: "egress:gb",
		ExpiresAt: original, MaxBytes: 1 << 20}
	if response := manager.ExecuteModemData(context.Background(), prepare); response.Failure != nil {
		t.Fatalf("prepare=%+v", response)
	}
	extended := time.Now().UTC().Add(300 * time.Millisecond)
	renew := prepare
	renew.OperationID = "renew-a"
	renew.Action = agentlink.ModemDataRenew
	renew.ExpiresAt = extended
	response := manager.ExecuteModemData(context.Background(), renew)
	if response.Failure != nil || response.ExpiresAt == nil || !response.ExpiresAt.Equal(extended) {
		t.Fatalf("renew=%+v", response)
	}
	time.Sleep(time.Until(original) + 40*time.Millisecond)
	if err := manager.DoAuxiliary(context.Background(), prepare.EquipmentID, func(context.Context) error { return nil }); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("session expired at original deadline: %v", err)
	}
	stop := prepare
	stop.OperationID = "stop-renew"
	stop.Action = agentlink.ModemDataStop
	stop.ExpiresAt = time.Time{}
	stop.MaxBytes = 0
	if response := manager.ExecuteModemData(context.Background(), stop); response.Failure != nil {
		t.Fatalf("stop=%+v", response)
	}
	backend.mu.Lock()
	stops := backend.stops
	backend.mu.Unlock()
	if stops != 1 {
		t.Fatalf("backend stops=%d", stops)
	}
}

func TestBrokerSessionRenewalPreventsOldExpiryPurge(t *testing.T) {
	now := time.Now().UTC()
	clock := now
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) {
		return managerTestToken, nil
	}), func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	oldExpiry := now.Add(time.Minute)
	newExpiry := now.Add(2 * time.Minute)
	if err := broker.Reserve(Reservation{AgentID: "agent-a", ProcessGeneration: "process-a",
		SessionID: "session-renew", StreamID: "stream-renew", StreamToken: managerTestToken,
		Network: "tcp", ExpiresAt: oldExpiry}); err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer right.Close()
	broker.mu.Lock()
	record := broker.items["stream-renew"]
	record.conn = newTrackedConn(left)
	close(record.ready)
	broker.mu.Unlock()
	acquired, err := broker.Acquire(context.Background(), "stream-renew")
	if err != nil {
		t.Fatal(err)
	}
	defer acquired.Close()
	if err := broker.RenewSession("session-renew", newExpiry); err != nil {
		t.Fatal(err)
	}
	clock = oldExpiry.Add(time.Second)
	if err := broker.Reserve(Reservation{AgentID: "agent-a", ProcessGeneration: "process-a",
		SessionID: "trigger", StreamID: "trigger-purge", StreamToken: managerTestToken,
		Network: "tcp", ExpiresAt: newExpiry}); err != nil {
		t.Fatal(err)
	}
	broker.mu.Lock()
	retained := broker.items["stream-renew"]
	broker.mu.Unlock()
	if retained == nil || !retained.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("renewed reservation was purged or stale: %+v", retained)
	}
	message := []byte("still-open")
	go func() { _, _ = right.Write(message) }()
	_ = acquired.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, len(message))
	if _, err := io.ReadFull(acquired, got); err != nil || string(got) != string(message) {
		t.Fatalf("renewed connection read=%q err=%v", got, err)
	}
	clock = now
	if err := broker.RenewSession("session-renew", oldExpiry); err == nil {
		t.Fatal("reservation deadline was allowed to move backwards")
	}
}

func TestBrokerDisconnectAgentRevokesOnlyItsDataStreams(t *testing.T) {
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) {
		return managerTestToken, nil
	}), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []Reservation{
		{AgentID: "agent-a", ProcessGeneration: "process-a", SessionID: "session-a", StreamID: "stream-a", StreamToken: managerTestToken, Network: "tcp", ExpiresAt: time.Now().Add(time.Minute)},
		{AgentID: "agent-b", ProcessGeneration: "process-b", SessionID: "session-b", StreamID: "stream-b", StreamToken: managerTestToken, Network: "udp", ExpiresAt: time.Now().Add(time.Minute)},
	} {
		if err := broker.Reserve(input); err != nil {
			t.Fatal(err)
		}
	}
	broker.DisconnectAgent("agent-a")
	broker.mu.Lock()
	_, removed := broker.items["stream-a"]
	_, retained := broker.items["stream-b"]
	broker.mu.Unlock()
	if removed || !retained {
		t.Fatalf("removed=%v retained=%v", removed, retained)
	}
}
