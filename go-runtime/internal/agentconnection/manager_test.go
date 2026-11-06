package agentconnection

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
)

type testBackend struct {
	prepares, stops, stopFailures, prepareFailures int
	invalidated                                    chan struct{}
}

func TestDisabledConnectionReconcilesHostBearerWithoutLocalOwner(t *testing.T) {
	backend := &testBackend{stopFailures: 1}
	manager, _ := New(backend)
	target := agentdata.Target{AttachmentID: "attachment", EquipmentID: "equipment", CardID: "card", SIMSessionGeneration: "generation"}
	if err := manager.SetPersistent(context.Background(), target, agentdata.Profile{}, false); err == nil {
		t.Fatal("host bearer failure was reported as ready")
	}
	if err := manager.SetPersistent(context.Background(), target, agentdata.Profile{}, false); err != nil {
		t.Fatal(err)
	}
	if backend.stops != 2 || backend.prepares != 0 || len(manager.OwnedTargets()) != 0 {
		t.Fatal("off intent created ownership or enabled data")
	}
}

func (value *testBackend) PrepareData(context.Context, agentdata.Target, agentdata.Profile) (string, error) {
	value.prepares++
	if value.prepareFailures > 0 {
		value.prepareFailures--
		return "", errors.New("partial prepare rollback")
	}
	value.invalidated = make(chan struct{})
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

func TestBorrowerRecoveryPreservesLogicalOwnerAcrossPartialFailure(t *testing.T) {
	ctx := context.Background()
	backend := &testBackend{stopFailures: 1}
	manager, _ := New(backend)
	target := agentdata.Target{AttachmentID: "attachment", EquipmentID: "equipment", CardID: "card", SIMSessionGeneration: "generation"}
	profile := agentdata.Profile{Name: "carrier", APN: "internet"}
	if err := manager.SetPersistent(ctx, target, profile, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareData(ctx, target, profile); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverData(ctx, target, profile); err == nil {
		t.Fatal("failed physical stop was hidden")
	}
	if len(manager.OwnedTargets()) != 1 {
		t.Fatal("partial failure lost original owner")
	}
	if err := manager.RecoverData(ctx, target, profile); err != nil {
		t.Fatal(err)
	}
	if backend.prepares != 2 || backend.stops != 2 || !manager.PersistentMatches(target, profile) {
		t.Fatal("recovery lost persistent intent or did not restore bearer")
	}
	if err := manager.ReleaseStale(ctx, target); !errors.Is(err, agentdata.ErrSessionActive) {
		t.Fatal("recovery dropped borrowed ownership")
	}
	replacement := target
	replacement.SIMSessionGeneration = "replacement"
	if err := manager.RecoverData(ctx, replacement, profile); err == nil || backend.prepares != 2 || backend.stops != 2 {
		t.Fatal("recovery crossed SIM identity")
	}
	if err := manager.StopData(ctx, target); err != nil || backend.stops != 2 {
		t.Fatal("borrower stop disrupted retained persistent intent")
	}
}

func (b *testBackend) DataValidity(agentdata.Target) (<-chan struct{}, error) {
	return b.invalidated, nil
}

func TestCoexistenceConnectionRecoveryRetainsInvalidatedAndPartialOwners(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback complete", true: "rollback pending"}[rollbackFails], func(t *testing.T) {
			b := &testBackend{}
			m, _ := New(b)
			ctx := t.Context()
			target := agentdata.Target{AttachmentID: "attachment", EquipmentID: "equipment", CardID: "card", SIMSessionGeneration: "generation"}
			profile := agentdata.Profile{Name: "carrier"}
			if _, err := m.PrepareData(ctx, target, profile); err != nil {
				t.Fatal(err)
			}
			close(b.invalidated)
			b.prepareFailures = 1
			if err := m.RecoverData(ctx, target, profile); err == nil {
				t.Fatal("partial prepare error hidden")
			}
			if b.prepares != 2 {
				t.Fatal("invalidated owner could not attempt recovery")
			}
			if rollbackFails {
				b.stopFailures = 1
				if err := m.RecoverData(ctx, target, profile); err == nil {
					t.Fatal("rollback failure hidden")
				}
			}
			if _, err := m.PrepareData(ctx, target, profile); err == nil {
				t.Fatal("new borrower stole partial recovery")
			}
			if err := m.RecoverData(ctx, target, profile); err != nil {
				t.Fatalf("partial owner failed to recover: %v", err)
			}
			if b.prepares != 3 {
				t.Fatal("original owner was not restored")
			}
			if err := m.ReleaseStale(ctx, target); !errors.Is(err, agentdata.ErrSessionActive) {
				t.Fatal("recovery lost borrower")
			}
		})
	}
}
