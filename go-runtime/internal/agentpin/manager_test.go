package agentpin

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type fakePINRuntime struct {
	calls  int
	result agentmodem.SIMPINResult
	err    error
}

func (runtime *fakePINRuntime) EnterSIMPIN(context.Context, agentmodem.SIMPINRequest) (agentmodem.SIMPINResult, error) {
	runtime.calls++
	return runtime.result, runtime.err
}

type fakeCoordinator struct{}

func (fakeCoordinator) DoAuxiliary(ctx context.Context, _ string, callback func(context.Context) error) error {
	return callback(ctx)
}

func lockedFact() agentmodem.Fact {
	remaining := uint32(3)
	return agentmodem.Fact{
		AttachmentID: "attachment-1", EquipmentID: "123456789012345",
		SIM: agentmodem.SIMFact{
			State: agentmodem.SIMLocked, ICCID: "89010000000000000001",
			PINState: "pin_required", PINAttempts: &remaining,
		},
	}
}

func TestSuccessfulPINCanRunAgainAfterARealRelock(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "pin.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &fakePINRuntime{result: agentmodem.SIMPINResult{Attempted: true, Ready: true}}
	manager, _ := NewManager(store, runtime, fakeCoordinator{}, map[string]string{"89010000000000000001": "1234"}, map[string]string{"89010000000000000001": "revision-1"})
	for iteration := 0; iteration < 2; iteration++ {
		facts := []agentmodem.Fact{lockedFact()}
		if err := manager.RecoverPINs(context.Background(), facts); err != nil {
			t.Fatal(err)
		}
		if facts[0].SIM.PINRecovery != "unlocked" {
			t.Fatalf("recovery=%q", facts[0].SIM.PINRecovery)
		}
	}
	if runtime.calls != 2 {
		t.Fatalf("PIN attempts=%d, want 2 confirmed attempts after two observed relocks", runtime.calls)
	}
}

func TestUncertainPINIsPersistentlyBlockedUntilConfigurationRevisionChanges(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "pin.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &fakePINRuntime{result: agentmodem.SIMPINResult{Attempted: true}, err: errors.New("response lost")}
	manager, _ := NewManager(store, runtime, fakeCoordinator{}, map[string]string{"89010000000000000001": "1234"}, map[string]string{"89010000000000000001": "revision-1"})
	for iteration := 0; iteration < 2; iteration++ {
		facts := []agentmodem.Fact{lockedFact()}
		if err := manager.RecoverPINs(context.Background(), facts); err != nil {
			t.Fatal(err)
		}
		if facts[0].SIM.PINRecovery != "blocked" {
			t.Fatalf("recovery=%q", facts[0].SIM.PINRecovery)
		}
	}
	if runtime.calls != 1 {
		t.Fatalf("same uncertain PIN was attempted %d times", runtime.calls)
	}
	manager, _ = NewManager(store, runtime, fakeCoordinator{}, map[string]string{"89010000000000000001": "5678"}, map[string]string{"89010000000000000001": "revision-2"})
	facts := []agentmodem.Fact{lockedFact()}
	if err := manager.RecoverPINs(context.Background(), facts); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 2 {
		t.Fatalf("changed PIN revision did not allow one explicit new attempt: %d", runtime.calls)
	}
}

func TestPINRecoveryRefusesUnknownOrLastCounter(t *testing.T) {
	store, _ := Open(filepath.Join(t.TempDir(), "pin.db"), time.Second)
	defer store.Close()
	runtime := &fakePINRuntime{}
	manager, _ := NewManager(store, runtime, fakeCoordinator{}, map[string]string{"89010000000000000001": "1234"}, map[string]string{"89010000000000000001": "revision-1"})
	fact := lockedFact()
	fact.SIM.PINAttempts = nil
	facts := []agentmodem.Fact{fact}
	if err := manager.RecoverPINs(context.Background(), facts); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 0 || facts[0].SIM.PINRecovery != "blocked" {
		t.Fatalf("calls=%d recovery=%q", runtime.calls, facts[0].SIM.PINRecovery)
	}
}

func TestChangingAnotherCardRevisionDoesNotUnblockThisCard(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "pin.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &fakePINRuntime{result: agentmodem.SIMPINResult{Attempted: true}, err: errors.New("response lost")}
	cardID := "89010000000000000001"
	manager, _ := NewManager(store, runtime, fakeCoordinator{},
		map[string]string{cardID: "1234", "89010000000000000002": "5678"},
		map[string]string{cardID: "revision-1", "89010000000000000002": "revision-1"})
	facts := []agentmodem.Fact{lockedFact()}
	if err := manager.RecoverPINs(context.Background(), facts); err != nil {
		t.Fatal(err)
	}
	manager, _ = NewManager(store, runtime, fakeCoordinator{},
		map[string]string{cardID: "1234", "89010000000000000002": "0000"},
		map[string]string{cardID: "revision-1", "89010000000000000002": "revision-2"})
	facts = []agentmodem.Fact{lockedFact()}
	if err := manager.RecoverPINs(context.Background(), facts); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 || facts[0].SIM.PINRecovery != "blocked" {
		t.Fatalf("other card revision unblocked this card: calls=%d recovery=%q", runtime.calls, facts[0].SIM.PINRecovery)
	}
}
