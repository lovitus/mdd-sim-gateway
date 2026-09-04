package agentconnection

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
)

type testBackend struct{ prepares, stops, stopFailures int }

func (value *testBackend) PrepareData(context.Context, agentdata.Target, agentdata.Profile) (string, error) {
	value.prepares++
	return "carrier", nil
}
func (*testBackend) DialData(context.Context, agentdata.Target, string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}
func (value *testBackend) StopData(context.Context, agentdata.Target) error {
	value.stops++
	if value.stopFailures > 0 {
		value.stopFailures--
		return errors.New("injected stop failure")
	}
	return nil
}

func TestPersistentConnectionSharesBearerWithoutBorrowStopDisconnect(t *testing.T) {
	backend := &testBackend{}
	manager, err := New(backend)
	if err != nil {
		t.Fatal(err)
	}
	target := agentdata.Target{AttachmentID: "attachment", EquipmentID: "equipment", CardID: "card", SIMSessionGeneration: "generation"}
	profile := agentdata.Profile{Name: "carrier", APN: "internet"}
	if err := manager.SetPersistent(context.Background(), target, profile, true); err != nil {
		t.Fatal(err)
	}
	if name, err := manager.PrepareData(context.Background(), target, profile); err != nil || name != "carrier" || backend.prepares != 1 {
		t.Fatalf("borrow name=%q prepares=%d err=%v", name, backend.prepares, err)
	}
	if err := manager.SetPersistent(context.Background(), target, profile, false); !errors.Is(err, agentdata.ErrSessionActive) {
		t.Fatalf("active borrow did not block persistent disable: %v", err)
	}
	if err := manager.StopData(context.Background(), target); err != nil || backend.stops != 0 || !manager.Persistent(target) {
		t.Fatalf("borrow stop disconnected persistent bearer stops=%d persistent=%t err=%v", backend.stops, manager.Persistent(target), err)
	}
	if err := manager.SetPersistent(context.Background(), target, profile, false); err != nil || backend.stops != 1 {
		t.Fatalf("persistent disable stops=%d err=%v", backend.stops, err)
	}
}

func TestReleaseStaleRetainsFailedOrBorrowedOwnerUntilSafeCleanup(t *testing.T) {
	target := agentdata.Target{AttachmentID: "attachment", EquipmentID: "equipment", CardID: "card-a", SIMSessionGeneration: "generation-a"}
	profile := agentdata.Profile{Name: "carrier"}
	backend := &testBackend{stopFailures: 1}
	manager, _ := New(backend)
	if err := manager.SetPersistent(context.Background(), target, profile, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReleaseStale(context.Background(), target); err == nil || len(manager.OwnedTargets()) != 1 {
		t.Fatalf("failed cleanup dropped owner err=%v targets=%+v", err, manager.OwnedTargets())
	}
	if err := manager.ReleaseStale(context.Background(), target); err != nil || len(manager.OwnedTargets()) != 0 || backend.stops != 2 {
		t.Fatalf("retry cleanup err=%v targets=%+v stops=%d", err, manager.OwnedTargets(), backend.stops)
	}

	backend = &testBackend{}
	manager, _ = New(backend)
	if err := manager.SetPersistent(context.Background(), target, profile, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareData(context.Background(), target, profile); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReleaseStale(context.Background(), target); !errors.Is(err, agentdata.ErrSessionActive) || backend.stops != 0 {
		t.Fatalf("borrowed stale owner was stopped err=%v stops=%d", err, backend.stops)
	}
}
