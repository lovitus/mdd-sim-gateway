package agentrawusb

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRecoveryStorePersistsAndClearsOnlyExactHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "raw-modem-handoffs.db")
	store, err := OpenRecoveryStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	record := testRecoveryRecord()
	got, created, err := store.Arm(record)
	if err != nil || !created || !sameRecoveryIdentity(got, record) {
		t.Fatalf("arm got=%+v created=%t err=%v", got, created, err)
	}
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode=%v err=%v", info.Mode().Perm(), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenRecoveryStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.Records()
	if err != nil || len(records) != 1 || !sameRecoveryIdentity(records[0], record) {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	stale := record
	stale.USBSessionID = "usb-session-new"
	if err := store.ClearExpected(stale); !errors.Is(err, ErrRecoveryMismatch) {
		t.Fatalf("stale clear err=%v", err)
	}
	if err := store.ClearExpected(record); err != nil {
		t.Fatal(err)
	}
	if records, err := store.Records(); err != nil || len(records) != 0 {
		t.Fatalf("records after clear=%+v err=%v", records, err)
	}
}

func TestRecoveryStoreArmIsIdempotentButReplacementConflicts(t *testing.T) {
	store, err := OpenRecoveryStore(filepath.Join(t.TempDir(), "state", "raw-modem-handoffs.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := testRecoveryRecord()
	if _, created, err := store.Arm(record); err != nil || !created {
		t.Fatalf("first arm created=%t err=%v", created, err)
	}
	if got, created, err := store.Arm(record); err != nil || created || !sameRecoveryIdentity(got, record) {
		t.Fatalf("idempotent arm got=%+v created=%t err=%v", got, created, err)
	}
	replacement := record
	replacement.CardID = "8944100000000000002"
	if _, _, err := store.Arm(replacement); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("replacement arm err=%v", err)
	}
}

func TestRecoveryStoreBindsExactUSBIdentityBeforeCapture(t *testing.T) {
	store, err := OpenRecoveryStore(filepath.Join(t.TempDir(), "state", "raw-modem-handoffs.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := testRecoveryRecord()
	if _, _, err := store.Arm(record); err != nil {
		t.Fatal(err)
	}
	target := SourceTarget{
		SourceAgentID: record.SourceAgentID, SourceProcessGeneration: record.SourceProcessGeneration,
		AttachmentID: record.AttachmentID, SessionGeneration: record.SessionGeneration,
		EquipmentID: record.EquipmentID, CardID: record.CardID, USBSessionID: record.USBSessionID,
	}
	identity := RecoveryUSBIdentity{StableID: "windows-instance:exact", BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125}
	updated, err := store.BindUSBIdentity(target, identity)
	if err != nil || updated.USB == nil || *updated.USB != identity {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if got, err := store.BindUSBIdentity(target, identity); err != nil || got.USB == nil || *got.USB != identity {
		t.Fatalf("idempotent bind=%+v err=%v", got, err)
	}
	replacement := identity
	replacement.BusID = "3-3"
	if _, err := store.BindUSBIdentity(target, replacement); !errors.Is(err, ErrRecoveryMismatch) {
		t.Fatalf("replacement USB identity err=%v", err)
	}
	records, err := store.Records()
	if err != nil || len(records) != 1 || records[0].USB == nil || *records[0].USB != identity {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if err := store.ClearExpected(records[0]); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryReleaseRequiresExplicitExactIntent(t *testing.T) {
	store, err := OpenRecoveryStore(filepath.Join(t.TempDir(), "state", "raw-modem-handoffs.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := testRecoveryRecord()
	if _, _, err := store.Arm(record); err != nil {
		t.Fatal(err)
	}
	target := SourceTarget{
		SourceAgentID: record.SourceAgentID, SourceProcessGeneration: record.SourceProcessGeneration,
		AttachmentID: record.AttachmentID, SessionGeneration: record.SessionGeneration,
		EquipmentID: record.EquipmentID, CardID: record.CardID, USBSessionID: record.USBSessionID,
	}
	requested, err := store.RequestRelease(target)
	if err != nil || !requested.ReleaseRequested {
		t.Fatalf("requested=%+v err=%v", requested, err)
	}
	if requested, err = store.RequestRelease(target); err != nil || !requested.ReleaseRequested {
		t.Fatalf("idempotent requested=%+v err=%v", requested, err)
	}
	stale := target
	stale.CardID = "8944100000000000002"
	if _, err := store.RequestRelease(stale); !errors.Is(err, ErrRecoveryMismatch) {
		t.Fatalf("stale release err=%v", err)
	}
}

func testRecoveryRecord() RecoveryRecord {
	return RecoveryRecord{
		SchemaVersion: recoveryStoreSchema, SourceAgentID: "source-agent",
		SourceProcessGeneration: "source-process", AttachmentID: "attachment-old",
		SessionGeneration: "sim-generation", EquipmentID: "867530900000001",
		CardID: "8944100000000000001", USBSessionID: "usb-session-a",
		ArmedAt: time.Date(2026, 8, 31, 1, 2, 3, 0, time.UTC),
	}
}
