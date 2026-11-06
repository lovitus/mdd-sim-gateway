package agentdata

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

// Synthetic platform boundary; session admission, expiry, recovery and socket
// cancellation remain the production manager.
type recoveryHardware struct {
	fakeBackend
	validity   chan struct{}
	recoveries int
	partial    bool
}

func (b *recoveryHardware) DataValidity(Target) (<-chan struct{}, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.validity, nil
}
func (b *recoveryHardware) RecoverData(context.Context, Target, Profile) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recoveries++
	if b.partial {
		b.partial = false
		return errors.New("prepare failed; rollback pending")
	}
	b.validity = make(chan struct{})
	return nil
}

type recoveryAdmission struct {
	passAdmission
	mu           sync.Mutex
	disconnected bool
}

func (a *recoveryAdmission) DataRecoveryNeeded(context.Context, Target) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.disconnected, nil
}

func TestCoexistenceInvalidDataDoesNotCancelVoiceOrLease(t *testing.T) {
	b := &recoveryHardware{validity: make(chan struct{})}
	a := &recoveryAdmission{}
	m, err := NewManager(Config{Context: t.Context(), ServerURL: "http://127.0.0.1:1", ServerToken: managerTestToken,
		AgentID: "agent", ProcessGeneration: "process", HTTPClient: http.DefaultClient, Backend: b, Coordinator: passCoordinator{}, Admission: a})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	r := agentlink.ModemDataRequest{OperationID: "prepare", AttachmentID: "attachment", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		SIMSessionGeneration: "generation", Action: agentlink.ModemDataPrepare, SessionID: "session", ExpiresAt: time.Now().Add(time.Minute), MaxBytes: 4096}
	if out := m.ExecuteModemData(t.Context(), r); out.Failure != nil {
		t.Fatal(out)
	}
	b.mu.Lock()
	close(b.validity)
	b.mu.Unlock()
	called := false
	if err := m.DoCall(t.Context(), r.EquipmentID, func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("invalid data blocked voice: %v", err)
	}
	r.Purpose = "manual"
	r.Action = agentlink.ModemDataRenew
	r.OperationID = "renew"
	r.ExpiresAt = r.ExpiresAt.Add(time.Minute)
	if out := m.ExecuteModemData(t.Context(), r); out.Failure != nil {
		t.Fatalf("invalid data cancelled logical lease: %+v", out)
	}
	m.mu.Lock()
	current := m.items[r.EquipmentID]
	m.mu.Unlock()
	if current == nil || current.ctx.Err() != nil {
		t.Fatal("data invalidation ended logical owner")
	}
	// Even before watcher observes the closed channel, recovery must recognize it.
	m.recoverConnection(current)
	b.mu.Lock()
	recovered := b.recoveries
	b.mu.Unlock()
	if recovered != 1 {
		t.Fatalf("invalidated claim did not recover: %d", recovered)
	}
}

func TestCoexistencePartialRecoveryRetainsDebtWhenObservationIsUnknown(t *testing.T) {
	b := &recoveryHardware{validity: make(chan struct{}), partial: true}
	a := &recoveryAdmission{disconnected: true}
	m, err := NewManager(Config{Context: t.Context(), ServerURL: "http://127.0.0.1:1", ServerToken: managerTestToken,
		AgentID: "agent", ProcessGeneration: "process", HTTPClient: http.DefaultClient, Backend: b, Coordinator: passCoordinator{}, Admission: a})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	r := agentlink.ModemDataRequest{OperationID: "prepare", AttachmentID: "attachment", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: agentlink.ModemDataPrepare, SessionID: "session", ExpiresAt: time.Now().Add(time.Minute), MaxBytes: 4096}
	if out := m.ExecuteModemData(t.Context(), r); out.Failure != nil {
		t.Fatal(out)
	}
	m.mu.Lock()
	current := m.items[r.EquipmentID]
	m.mu.Unlock()
	m.recoverConnection(current)
	a.mu.Lock()
	a.disconnected = false
	a.mu.Unlock()
	m.recoverConnection(current)
	b.mu.Lock()
	count := b.recoveries
	b.mu.Unlock()
	if count != 2 {
		t.Fatalf("partial recovery debt stranded: attempts=%d", count)
	}
	// A fresh unknown observation, with no retained debt, must not authorize reset.
	m.recoverConnection(current)
	b.mu.Lock()
	count = b.recoveries
	b.mu.Unlock()
	if count != 2 {
		t.Fatal("ordinary unknown authorized physical recovery")
	}
}

type slowDialHardware struct {
	fakeBackend
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *slowDialHardware) DialData(ctx context.Context, target Target, network, address string) (net.Conn, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release // A late platform result must not become an admitted stream.
	return b.fakeBackend.DialData(ctx, target, network, address)
}
func TestCoexistenceSlowOpenDoesNotDelayVoiceAndStopRejectsLateSocket(t *testing.T) {
	b := &slowDialHardware{started: make(chan struct{}), release: make(chan struct{})}
	m, err := NewManager(Config{Context: t.Context(), ServerURL: "http://127.0.0.1:1", ServerToken: managerTestToken,
		AgentID: "agent", ProcessGeneration: "process", HTTPClient: http.DefaultClient, Backend: b, Coordinator: passCoordinator{}, Admission: passAdmission{}})
	if err != nil {
		t.Fatal(err)
	}
	var release sync.Once
	defer m.Close()
	defer release.Do(func() { close(b.release) })
	r := agentlink.ModemDataRequest{OperationID: "prepare", AttachmentID: "attachment", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: agentlink.ModemDataPrepare, SessionID: "session", ExpiresAt: time.Now().Add(time.Minute), MaxBytes: 4096}
	if out := m.ExecuteModemData(t.Context(), r); out.Failure != nil {
		t.Fatal(out)
	}
	open := r
	open.Action = agentlink.ModemDataOpen
	open.OperationID = "open"
	open.StreamID = "stream"
	open.StreamToken = managerTestToken
	open.Network = "tcp"
	open.Address = "192.0.2.1:80"
	done := make(chan agentlink.ModemDataResponse, 1)
	go func() { done <- m.ExecuteModemData(t.Context(), open) }()
	select {
	case <-b.started:
	case <-time.After(time.Second):
		t.Fatal("open did not enter platform dial")
	}
	voice := make(chan error, 1)
	go func() { voice <- m.DoCall(t.Context(), r.EquipmentID, func(context.Context) error { return nil }) }()
	select {
	case err := <-voice:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow data dial blocked voice admission")
	}
	stop := r
	stop.ExpiresAt, stop.MaxBytes = time.Time{}, 0
	stop.Action = agentlink.ModemDataStop
	stop.OperationID = "stop"
	if out := m.ExecuteModemData(t.Context(), stop); out.Failure != nil {
		t.Fatal(out)
	}
	release.Do(func() { close(b.release) })
	select {
	case out := <-done:
		if out.Failure == nil {
			t.Fatal("late data socket survived stop")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled open did not finish")
	}
	b.mu.Lock()
	peer := b.peers[0]
	b.mu.Unlock()
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("late socket not closed")
	}
}
