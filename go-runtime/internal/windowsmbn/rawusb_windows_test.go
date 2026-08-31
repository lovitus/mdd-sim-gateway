//go:build windows && (amd64 || arm64)

package windowsmbn

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowsdataguard"
)

func TestAcquireRawUSBArmsDebtBeforeATRelease(t *testing.T) {
	prober, store, target := rawWindowsTestProber(t)
	releaseCalled := false
	prober.rawReleaseAT = func(equipmentID string) (string, error) {
		releaseCalled = true
		records, err := store.Records()
		if err != nil || len(records) != 1 || records[0].EquipmentID != equipmentID || records[0].CardID != target.CardID {
			t.Fatalf("debt was not durable before AT release: records=%+v err=%v", records, err)
		}
		return `USB\VID_2C7C&PID_0125\MODEM`, nil
	}
	physicalID, err := prober.AcquireRawUSB(context.Background(), target)
	if err != nil || !releaseCalled || physicalID == "" || prober.raw[target.EquipmentID].physicalID != physicalID {
		t.Fatalf("physical=%q release=%t raw=%+v err=%v", physicalID, releaseCalled, prober.raw, err)
	}
}

func TestAcquireRawUSBRejectsFreshLockedSIMBeforeDebtOrRelease(t *testing.T) {
	prober, store, target := rawWindowsTestProber(t)
	prober.freshSIMPINStatus = func(context.Context, string) (agentat.SIMPINStatus, error) {
		return agentat.SIMPINStatus{State: agentat.SIMPINRequired, CardID: target.CardID}, nil
	}
	releaseCalled := false
	prober.rawReleaseAT = func(string) (string, error) { releaseCalled = true; return "", nil }
	_, err := prober.AcquireRawUSB(context.Background(), target)
	records, recordsErr := store.Records()
	if !errors.Is(err, agentmodem.ErrOperationTargetReplaced) || releaseCalled || recordsErr != nil || len(records) != 0 {
		t.Fatalf("err=%v release=%t records=%+v recordsErr=%v", err, releaseCalled, records, recordsErr)
	}
}

func TestAcquireRawUSBRetainsDebtWhenATReleaseFails(t *testing.T) {
	prober, store, target := rawWindowsTestProber(t)
	prober.rawReleaseAT = func(string) (string, error) { return "", errors.New("release failed") }
	if _, err := prober.AcquireRawUSB(context.Background(), target); err == nil {
		t.Fatal("AT release failure was accepted")
	}
	records, err := store.Records()
	if err != nil || len(records) != 1 || records[0].EquipmentID != target.EquipmentID || len(prober.raw) != 0 {
		t.Fatalf("records=%+v raw=%+v err=%v", records, prober.raw, err)
	}
}

func TestFreshPaidActionCardProofRejectsChangedOrLockedSIM(t *testing.T) {
	prober, _, target := rawWindowsTestProber(t)
	prober.freshSIMPINStatus = func(context.Context, string) (agentat.SIMPINStatus, error) {
		return agentat.SIMPINStatus{State: agentat.SIMPINNotRequired, CardID: "8944100000000000099"}, nil
	}
	if err := prober.requireFreshReadyCard(context.Background(), target.EquipmentID, target.CardID); !errors.Is(err, agentmodem.ErrOperationTargetReplaced) {
		t.Fatalf("changed-card proof error=%v", err)
	}
	prober.freshSIMPINStatus = func(context.Context, string) (agentat.SIMPINStatus, error) {
		return agentat.SIMPINStatus{State: agentat.SIMPINRequired, CardID: target.CardID}, nil
	}
	if err := prober.requireFreshReadyCard(context.Background(), target.EquipmentID, target.CardID); !errors.Is(err, agentmodem.ErrOperationTargetReplaced) {
		t.Fatalf("locked-card proof error=%v", err)
	}
}

func TestReleasedRawDebtRequiresExactCardAndTerminalHangup(t *testing.T) {
	t.Run("changed attachment exact card clears", func(t *testing.T) {
		prober, store, target := rawWindowsTestProber(t)
		record := recoveryRecord(target, time.Now().UTC())
		stored, _, err := store.Arm(record)
		if err != nil {
			t.Fatal(err)
		}
		record = stored
		hangups := 0
		prober.rawVerifiedHangup = func(context.Context, string) (agentat.CallState, error) {
			hangups++
			return agentat.CallState{State: "idle", Authoritative: true, TerminalConfirmed: true}, nil
		}
		fact := rawWindowsFact(target)
		fact.AttachmentID = "replacement-attachment-generation"
		prober.recoverRawHandoff(context.Background(), []agentmodem.Fact{fact}, record)
		records, err := store.Records()
		if err != nil || len(records) != 0 || hangups != 1 {
			t.Fatalf("records=%+v hangups=%d err=%v", records, hangups, err)
		}
	})

	t.Run("wrong card never hangs up or clears", func(t *testing.T) {
		prober, store, target := rawWindowsTestProber(t)
		record := recoveryRecord(target, time.Now().UTC())
		stored, _, err := store.Arm(record)
		if err != nil {
			t.Fatal(err)
		}
		record = stored
		prober.freshSIMPINStatus = func(context.Context, string) (agentat.SIMPINStatus, error) {
			return agentat.SIMPINStatus{State: agentat.SIMPINNotRequired, CardID: "8944100000000000099"}, nil
		}
		hangups := 0
		prober.rawVerifiedHangup = func(context.Context, string) (agentat.CallState, error) {
			hangups++
			return agentat.CallState{}, nil
		}
		prober.recoverRawHandoff(context.Background(), []agentmodem.Fact{rawWindowsFact(target)}, record)
		records, err := store.Records()
		if err != nil || len(records) != 1 || hangups != 0 {
			t.Fatalf("records=%+v hangups=%d err=%v", records, hangups, err)
		}
	})

	t.Run("nonterminal hangup proof remains armed", func(t *testing.T) {
		prober, store, target := rawWindowsTestProber(t)
		record := recoveryRecord(target, time.Now().UTC())
		stored, _, err := store.Arm(record)
		if err != nil {
			t.Fatal(err)
		}
		record = stored
		prober.rawVerifiedHangup = func(context.Context, string) (agentat.CallState, error) {
			return agentat.CallState{State: "idle", Authoritative: true}, nil
		}
		prober.recoverRawHandoff(context.Background(), []agentmodem.Fact{rawWindowsFact(target)}, record)
		records, err := store.Records()
		if err != nil || len(records) != 1 {
			t.Fatalf("records=%+v err=%v", records, err)
		}
	})

	t.Run("active export does not recover early", func(t *testing.T) {
		prober, store, target := rawWindowsTestProber(t)
		record := recoveryRecord(target, time.Now().UTC())
		stored, _, err := store.Arm(record)
		if err != nil {
			t.Fatal(err)
		}
		record = stored
		prober.raw[target.EquipmentID] = rawClaim{target: target, physicalID: "physical"}
		called := false
		prober.freshSIMPINStatus = func(context.Context, string) (agentat.SIMPINStatus, error) {
			called = true
			return agentat.SIMPINStatus{}, nil
		}
		prober.recoverRawHandoff(context.Background(), []agentmodem.Fact{rawWindowsFact(target)}, record)
		records, err := store.Records()
		if err != nil || len(records) != 1 || called {
			t.Fatalf("records=%+v probed=%t err=%v", records, called, err)
		}
	})
}

func rawWindowsTestProber(t *testing.T) (*Prober, *agentrawusb.RecoveryStore, agentrawusb.SourceTarget) {
	t.Helper()
	store, err := agentrawusb.OpenRecoveryStore(filepath.Join(t.TempDir(), "raw.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	target := agentrawusb.SourceTarget{
		SourceAgentID: "windows-source", SourceProcessGeneration: "process-generation",
		AttachmentID: "attachment-generation", SessionGeneration: "sim-generation",
		EquipmentID: "867530900000001", CardID: "8944100000000000001", USBSessionID: "usb-session",
	}
	prober := &Prober{
		guard: new(windowsdataguard.Guard), data: map[string]*dataBorrow{}, raw: map[string]rawClaim{},
		rawRecovery: store, sourceAgentID: target.SourceAgentID, recovery: map[string]rawRecoveryAttempt{},
	}
	prober.rawProbe = func(context.Context) ([]agentmodem.Fact, error) {
		return []agentmodem.Fact{rawWindowsFact(target)}, nil
	}
	prober.rawCallStatus = func(context.Context, string) (agentat.CallState, error) {
		return agentat.CallState{State: "idle", Authoritative: true}, nil
	}
	prober.freshSIMPINStatus = func(context.Context, string) (agentat.SIMPINStatus, error) {
		return agentat.SIMPINStatus{State: agentat.SIMPINNotRequired, CardID: target.CardID}, nil
	}
	prober.rawVerifiedHangup = func(context.Context, string) (agentat.CallState, error) {
		return agentat.CallState{State: "idle", Authoritative: true, TerminalConfirmed: true}, nil
	}
	prober.rawReleaseAT = func(string) (string, error) { return "physical", nil }
	return prober, store, target
}

func rawWindowsFact(target agentrawusb.SourceTarget) agentmodem.Fact {
	return agentmodem.Fact{
		AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID, Condition: agentmodem.DeviceReady,
		AT:  agentmodem.ATControlFact{State: agentmodem.ATControlReady},
		SIM: agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: target.CardID},
		Network: agentmodem.NetworkFact{
			Data: agentmodem.DataDisconnected, Guard: agentmodem.DataGuardFact{State: agentmodem.DataGuardProtected},
		},
	}
}
