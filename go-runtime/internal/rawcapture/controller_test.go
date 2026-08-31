package rawcapture

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
)

type fakeBackend struct {
	proof       Proof
	proveErr    error
	proveCalls  int
	releaseAT   int
	verifyCalls int
}

func (backend *fakeBackend) ProveRawCapture(context.Context, Pair) (Proof, error) {
	backend.proveCalls++
	return backend.proof, backend.proveErr
}
func (backend *fakeBackend) ReleaseATForRawCapture(context.Context, Proof) (string, error) {
	backend.releaseAT++
	return backend.proof.PhysicalID, nil
}
func (backend *fakeBackend) VerifyAdapted(context.Context, Pair) error {
	backend.verifyCalls++
	return nil
}

type fakeExporter struct {
	device rawusb.Device
	closed int
}

type persistentFakeExporter struct {
	fakeExporter
	preserved int
}

func (exporter *persistentFakeExporter) Preserve() error {
	exporter.preserved++
	return nil
}

func (exporter *fakeExporter) Device() rawusb.Device                   { return exporter.device }
func (*fakeExporter) ServeMultiplexed(context.Context, net.Conn) error { return nil }
func (exporter *fakeExporter) Close() error {
	exporter.closed++
	return nil
}

func TestLocalRawModeCapturesWithoutCoreAndRemoteStopPreservesIt(t *testing.T) {
	controller, backend, exporter, pair := testController(t)
	if err := controller.SetRaw(pair); err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backend.proveCalls != 1 || backend.releaseAT != 1 || exporter.closed != 0 {
		t.Fatalf("prove=%d releaseAT=%d closed=%d", backend.proveCalls, backend.releaseAT, exporter.closed)
	}
	facts := controller.RecoveryRawUSB()
	if len(facts) != 1 || facts[0].EquipmentID != pair.EquipmentID || facts[0].CardID != pair.CardID ||
		facts[0].State != "capture_reserved" {
		t.Fatalf("capture facts=%+v", facts)
	}
	target := agentrawusb.SourceTarget{EquipmentID: pair.EquipmentID, CardID: pair.CardID,
		CaptureGeneration: facts[0].CaptureGeneration}
	if _, err := controller.AcquireRawUSB(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	borrowed, ok, err := controller.TakeRawUSB(target)
	if err != nil || !ok || borrowed != exporter {
		t.Fatalf("borrowed=%v ok=%t err=%v", borrowed, ok, err)
	}
	if err := controller.PreserveRawUSB(target, borrowed); err != nil {
		t.Fatal(err)
	}
	if err := controller.ReleaseRawUSB(target); err != nil || exporter.closed != 0 {
		t.Fatalf("remote transport stop released capture: closed=%d err=%v", exporter.closed, err)
	}
}

func TestAgentShutdownPreservesPersistentCaptureWithoutRelease(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "raw.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pair := Pair{EquipmentID: "867530900000001", CardID: "8944100000000000001"}
	backend := &fakeBackend{proof: Proof{Pair: pair, AttachmentID: "attachment",
		SessionGeneration: "session", PhysicalID: "physical"}}
	exporter := &persistentFakeExporter{fakeExporter: fakeExporter{
		device: rawusb.Device{BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125},
	}}
	controller, err := New(Config{Store: store, Backend: backend,
		Capture: func(context.Context, string) (agentrawusb.Exporter, error) { return exporter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.SetRaw(pair); err != nil || controller.reconcile(context.Background()) != nil {
		t.Fatal(err)
	}
	if err := controller.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if exporter.preserved != 1 || exporter.closed != 0 {
		t.Fatalf("preserved=%d closed=%d", exporter.preserved, exporter.closed)
	}
}

func TestOnlyLocalAdaptedMutationReleasesAndVerifiesCapture(t *testing.T) {
	controller, backend, exporter, pair := testController(t)
	if err := controller.SetRaw(pair); err != nil || controller.reconcile(context.Background()) != nil {
		t.Fatal(err)
	}
	if err := controller.SetAdapted(pair); err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exporter.closed != 1 || backend.verifyCalls != 1 {
		t.Fatalf("closed=%d verified=%d", exporter.closed, backend.verifyCalls)
	}
	snapshot, err := controller.Snapshot()
	if err != nil || len(snapshot.Desired) != 0 || len(snapshot.Captures) != 0 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestLocalAdaptedWaitsForBorrowedTransportBeforeRelease(t *testing.T) {
	controller, backend, exporter, pair := testController(t)
	if err := controller.SetRaw(pair); err != nil || controller.reconcile(context.Background()) != nil {
		t.Fatal(err)
	}
	facts := controller.RecoveryRawUSB()
	target := agentrawusb.SourceTarget{EquipmentID: pair.EquipmentID, CardID: pair.CardID,
		CaptureGeneration: facts[0].CaptureGeneration}
	borrowed, ok, err := controller.TakeRawUSB(target)
	if err != nil || !ok {
		t.Fatalf("borrowed=%v ok=%t err=%v", borrowed, ok, err)
	}
	if err := controller.SetAdapted(pair); err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(context.Background()); err == nil {
		t.Fatal("borrowed transport did not defer local release")
	}
	if exporter.closed != 0 || backend.verifyCalls != 0 {
		t.Fatalf("closed=%d verified=%d", exporter.closed, backend.verifyCalls)
	}
	if err := controller.PreserveRawUSB(target, borrowed); err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exporter.closed != 1 || backend.verifyCalls != 1 {
		t.Fatalf("closed=%d verified=%d", exporter.closed, backend.verifyCalls)
	}
}

func TestControllerAdoptsDurableCaptureAfterProcessRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "raw.db")
	pair := Pair{EquipmentID: "867530900000001", CardID: "8944100000000000001"}
	proof := Proof{Pair: pair, AttachmentID: "attachment", SessionGeneration: "session", PhysicalID: "physical"}
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetRaw(pair); err != nil {
		t.Fatal(err)
	}
	record, err := store.ArmCapture(proof, "capture-generation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	device := rawusb.Device{BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125}
	if _, err := store.CompleteCapture(pair, record.CaptureGeneration, device, time.Now()); err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{proof: proof}
	exporter := &fakeExporter{device: device}
	controller, err := New(Config{Store: store, Backend: backend,
		Capture: func(context.Context, string) (agentrawusb.Exporter, error) {
			return nil, errors.New("fresh capture must not run")
		},
		Adopt:   func(context.Context, rawusb.Device) (agentrawusb.Exporter, error) { return exporter, nil },
		Release: func(context.Context, rawusb.Device) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if reconcileErr := controller.reconcile(context.Background()); reconcileErr != nil {
		t.Fatalf("controller=%v reconcile=%v", controller, reconcileErr)
	}
	if backend.proveCalls != 0 || len(controller.RecoveryRawUSB()) != 1 {
		t.Fatalf("fresh proofs=%d facts=%+v", backend.proveCalls, controller.RecoveryRawUSB())
	}
}

func TestControllerRefreshesBusIDForSamePersistentCapture(t *testing.T) {
	root := t.TempDir()
	pair := Pair{EquipmentID: "867530900000001", CardID: "8944100000000000001"}
	proof := Proof{Pair: pair, AttachmentID: "attachment", SessionGeneration: "session", PhysicalID: "physical"}
	store, err := Open(filepath.Join(root, "raw.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetRaw(pair); err != nil {
		t.Fatal(err)
	}
	record, err := store.ArmCapture(proof, "capture-generation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	oldDevice := rawusb.Device{BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125,
		Backend: "windows-usbipd-v1", InstanceID: "instance", PersistentID: "guid"}
	if _, err := store.CompleteCapture(pair, record.CaptureGeneration, oldDevice, time.Now()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Snapshot()
	if err != nil || len(stored.Captures) != 1 {
		t.Fatal(err)
	}
	if stored.Captures[0].Device != oldDevice {
		t.Fatalf("stored device=%+v want=%+v", stored.Captures[0].Device, oldDevice)
	}
	newDevice := oldDevice
	newDevice.BusID = "4-7"
	if !rawusb.SamePersistentDevice(oldDevice, newDevice) {
		t.Fatal("test devices do not share persistent identity")
	}
	exporter := &fakeExporter{device: newDevice}
	controller, err := New(Config{Store: store, Backend: &fakeBackend{proof: proof},
		Adopt: func(context.Context, rawusb.Device) (agentrawusb.Exporter, error) { return exporter, nil },
		Capture: func(context.Context, string) (agentrawusb.Exporter, error) {
			return nil, errors.New("fresh capture must not run")
		}, Release: func(context.Context, rawusb.Device) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if reconcileErr := controller.reconcile(context.Background()); reconcileErr != nil {
		t.Fatalf("controller=%v reconcile=%v", controller, reconcileErr)
	}
	snapshot, err := controller.Snapshot()
	if err != nil || len(snapshot.Captures) != 1 || snapshot.Captures[0].Device != newDevice {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestPendingCaptureAlreadyForcedIsAdoptedWithoutFreshSIMProbe(t *testing.T) {
	store, pair, proof, record := pendingCaptureStore(t)
	backend := &fakeBackend{proof: proof}
	exporter := &fakeExporter{device: rawusb.Device{BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125,
		Backend: "windows-usbipd-v1", InstanceID: "instance", PersistentID: "guid"}}
	controller, err := New(Config{Store: store, Backend: backend,
		AdoptPending: func(context.Context, string) (agentrawusb.Exporter, error) { return exporter, nil },
		Capture: func(context.Context, string) (agentrawusb.Exporter, error) {
			t.Fatal("fresh capture ran for an already forced pending device")
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backend.proveCalls != 0 || backend.releaseAT != 0 {
		t.Fatalf("fresh prove=%d releaseAT=%d", backend.proveCalls, backend.releaseAT)
	}
	snapshot, err := controller.Snapshot()
	if err != nil || len(snapshot.Captures) != 1 || snapshot.Captures[0].Stage != StageReserved ||
		snapshot.Captures[0].CaptureGeneration != record.CaptureGeneration {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	_ = pair
}

func TestPendingCaptureAbsentRequiresFreshExactPairBeforeForce(t *testing.T) {
	store, pair, _, _ := pendingCaptureStore(t)
	proof := Proof{Pair: pair, AttachmentID: "fresh-attachment", SessionGeneration: "fresh-session", PhysicalID: "fresh-physical"}
	backend := &fakeBackend{proof: proof}
	exporter := &fakeExporter{device: rawusb.Device{BusID: "4-7", VendorID: 0x2c7c, ProductID: 0x0125}}
	captures := 0
	controller, err := New(Config{Store: store, Backend: backend,
		AdoptPending: func(context.Context, string) (agentrawusb.Exporter, error) {
			return nil, rawusb.ErrCaptureNotPresent
		}, Capture: func(_ context.Context, physicalID string) (agentrawusb.Exporter, error) {
			captures++
			if physicalID != proof.PhysicalID {
				t.Fatalf("physicalID=%q", physicalID)
			}
			return exporter, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.Snapshot()
	if err != nil || captures != 1 || backend.proveCalls != 1 || backend.releaseAT != 1 ||
		len(snapshot.Captures) != 1 || snapshot.Captures[0].Stage != StageReserved ||
		snapshot.Captures[0].AttachmentID != proof.AttachmentID ||
		snapshot.Captures[0].SessionGeneration != proof.SessionGeneration ||
		snapshot.Captures[0].PhysicalID != proof.PhysicalID {
		t.Fatalf("captures=%d prove=%d release=%d snapshot=%+v err=%v",
			captures, backend.proveCalls, backend.releaseAT, snapshot, err)
	}
}

func TestPendingCaptureAbsentDoesNotForceAfterPairChanged(t *testing.T) {
	store, _, proof, _ := pendingCaptureStore(t)
	backend := &fakeBackend{proof: proof, proveErr: errors.New("fresh equipment or ICCID changed")}
	captures := 0
	controller, err := New(Config{Store: store, Backend: backend,
		AdoptPending: func(context.Context, string) (agentrawusb.Exporter, error) {
			return nil, rawusb.ErrCaptureNotPresent
		}, Capture: func(context.Context, string) (agentrawusb.Exporter, error) {
			captures++
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(context.Background()); err == nil {
		t.Fatal("changed pair did not block pending capture")
	}
	if captures != 0 || backend.releaseAT != 0 {
		t.Fatalf("captures=%d releaseAT=%d", captures, backend.releaseAT)
	}
}

func pendingCaptureStore(t *testing.T) (*Store, Pair, Proof, Record) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "raw.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	pair := Pair{EquipmentID: "867530900000001", CardID: "8944100000000000001"}
	proof := Proof{Pair: pair, AttachmentID: "old-attachment", SessionGeneration: "old-session", PhysicalID: "old-physical"}
	if err := store.SetRaw(pair); err != nil {
		t.Fatal(err)
	}
	record, err := store.ArmCapture(proof, "capture-generation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return store, pair, proof, record
}

func TestRawModeRejectsDuplicateEquipmentOrICCID(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "raw.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := Pair{EquipmentID: "867530900000001", CardID: "8944100000000000001"}
	if err := store.SetRaw(first); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRaw(Pair{EquipmentID: first.EquipmentID, CardID: "8944100000000000002"}); !errors.Is(err, ErrModeChanged) {
		t.Fatalf("same equipment err=%v", err)
	}
	if err := store.SetRaw(Pair{EquipmentID: "867530900000002", CardID: first.CardID}); !errors.Is(err, ErrModeChanged) {
		t.Fatalf("same ICCID err=%v", err)
	}
}

func testController(t *testing.T) (*Controller, *fakeBackend, *fakeExporter, Pair) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "raw.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	pair := Pair{EquipmentID: "867530900000001", CardID: "8944100000000000001"}
	backend := &fakeBackend{proof: Proof{Pair: pair, AttachmentID: "attachment",
		SessionGeneration: "session", PhysicalID: "physical"}}
	exporter := &fakeExporter{device: rawusb.Device{BusID: "3-2", VendorID: 0x2c7c, ProductID: 0x0125}}
	controller, err := New(Config{Store: store, Backend: backend,
		Capture: func(context.Context, string) (agentrawusb.Exporter, error) { return exporter, nil },
		Adopt:   func(context.Context, rawusb.Device) (agentrawusb.Exporter, error) { return exporter, nil },
		Release: func(context.Context, rawusb.Device) error { exporter.closed++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return controller, backend, exporter, pair
}
