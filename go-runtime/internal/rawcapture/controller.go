package rawcapture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
)

type Backend interface {
	ProveRawCapture(context.Context, Pair) (Proof, error)
	ReleaseATForRawCapture(context.Context, Proof) (string, error)
	VerifyAdapted(context.Context, Pair) error
}

type Config struct {
	Store        *Store
	Backend      Backend
	Interval     time.Duration
	Now          func() time.Time
	Capture      func(context.Context, string) (agentrawusb.Exporter, error)
	AdoptPending func(context.Context, string) (agentrawusb.Exporter, error)
	Adopt        func(context.Context, rawusb.Device) (agentrawusb.Exporter, error)
	Release      func(context.Context, rawusb.Device) error
	Logf         func(string, ...any)
}

type Controller struct {
	store        *Store
	backend      Backend
	interval     time.Duration
	now          func() time.Time
	capture      func(context.Context, string) (agentrawusb.Exporter, error)
	adoptPending func(context.Context, string) (agentrawusb.Exporter, error)
	adopt        func(context.Context, rawusb.Device) (agentrawusb.Exporter, error)
	release      func(context.Context, rawusb.Device) error
	logf         func(string, ...any)

	mu        sync.Mutex
	exporters map[string]*heldCapture
	retries   map[string]retryState
	wake      chan struct{}
}

type heldCapture struct {
	pair       Pair
	generation string
	device     rawusb.Device
	exporter   agentrawusb.Exporter
	borrowed   bool
}

type retryState struct {
	failures uint32
	next     time.Time
}

func New(config Config) (*Controller, error) {
	if config.Store == nil || config.Backend == nil {
		return nil, errors.New("invalid raw capture controller dependencies")
	}
	if config.Interval == 0 {
		config.Interval = time.Second
	}
	if config.Interval < 100*time.Millisecond || config.Interval > time.Minute {
		return nil, errors.New("invalid raw capture controller interval")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Capture == nil {
		config.Capture = func(ctx context.Context, physicalID string) (agentrawusb.Exporter, error) {
			return rawusb.NewExporter(ctx, physicalID)
		}
	}
	if config.Adopt == nil {
		config.Adopt = func(ctx context.Context, device rawusb.Device) (agentrawusb.Exporter, error) {
			return rawusb.NewExporterFromDevice(ctx, device)
		}
	}
	if config.AdoptPending == nil {
		config.AdoptPending = func(ctx context.Context, physicalID string) (agentrawusb.Exporter, error) {
			return rawusb.NewExporterFromPendingCapture(ctx, physicalID)
		}
	}
	if config.Release == nil {
		config.Release = rawusb.ReleaseCapturedDevice
	}
	if config.Logf == nil {
		config.Logf = log.Printf
	}
	return &Controller{
		store: config.Store, backend: config.Backend, interval: config.Interval, now: config.Now,
		capture: config.Capture, adoptPending: config.AdoptPending, adopt: config.Adopt,
		release: config.Release, logf: config.Logf,
		exporters: map[string]*heldCapture{}, retries: map[string]retryState{}, wake: make(chan struct{}, 1),
	}, nil
}

// Run is local level-triggered capture ownership. Core connectivity is not a
// dependency and stopping this loop never closes or unbinds a raw capture.
func (controller *Controller) Run(ctx context.Context) error {
	ticker := time.NewTicker(controller.interval)
	defer ticker.Stop()
	for {
		if err := controller.reconcile(ctx); err != nil && ctx.Err() == nil {
			controller.logf("mdd-agent: local raw capture reconcile: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-controller.wake:
		}
	}
}

func (controller *Controller) SetRaw(pair Pair) error {
	if err := controller.store.SetRaw(pair); err != nil {
		return err
	}
	controller.signal()
	return nil
}

func (controller *Controller) SetAdapted(pair Pair) error {
	if err := controller.store.SetAdapted(pair, controller.now().UTC()); err != nil {
		return err
	}
	controller.signal()
	return nil
}

func (controller *Controller) Snapshot() (Snapshot, error) { return controller.store.Snapshot() }

// Close releases only the durable database handle. Exporters are deliberately
// not closed: normal application exit must leave the kernel capture in place.
func (controller *Controller) Close() error { return controller.store.Close() }

func (controller *Controller) Shutdown() error {
	if controller == nil {
		return nil
	}
	controller.mu.Lock()
	held := make([]*heldCapture, 0, len(controller.exporters))
	for key, current := range controller.exporters {
		if current != nil {
			held = append(held, current)
		}
		delete(controller.exporters, key)
	}
	controller.mu.Unlock()
	var failures []error
	for _, current := range held {
		if preserver, ok := current.exporter.(interface{ Preserve() error }); ok {
			failures = append(failures, preserver.Preserve())
		}
	}
	return errors.Join(append(failures, controller.store.Close())...)
}

func (controller *Controller) signal() {
	select {
	case controller.wake <- struct{}{}:
	default:
	}
}

func (controller *Controller) reconcile(ctx context.Context) error {
	snapshot, err := controller.store.Snapshot()
	if err != nil {
		return err
	}
	desired := make(map[string]Pair, len(snapshot.Desired))
	records := make(map[string]Record, len(snapshot.Captures))
	for _, pair := range snapshot.Desired {
		desired[pair.EquipmentID] = pair
	}
	for _, record := range snapshot.Captures {
		records[record.Pair.EquipmentID] = record
	}
	var failures []error
	for _, record := range snapshot.Captures {
		pair, wanted := desired[record.Pair.EquipmentID]
		if !wanted || pair != record.Pair || record.Stage == StageReleasePending {
			if err := controller.releaseOne(ctx, record); err != nil {
				failures = append(failures, err)
			}
		}
	}
	for _, pair := range snapshot.Desired {
		record, exists := records[pair.EquipmentID]
		if exists && record.Stage == StageReleasePending {
			continue
		}
		if !controller.retryReady(pair) {
			continue
		}
		var err error
		switch {
		case !exists:
			err = controller.captureOne(ctx, pair, Record{})
		case record.Stage == StageCapturePending:
			err = controller.captureOne(ctx, pair, record)
		case record.Stage == StageReserved:
			err = controller.adoptOne(ctx, record)
		}
		if err != nil {
			controller.failed(pair)
			failures = append(failures, err)
		} else {
			controller.succeeded(pair)
		}
	}
	return errors.Join(failures...)
}

func (controller *Controller) captureOne(ctx context.Context, pair Pair, existing Record) error {
	controller.mu.Lock()
	if controller.exporters[pair.EquipmentID] != nil {
		controller.mu.Unlock()
		return nil
	}
	controller.mu.Unlock()
	record := existing
	physicalID := existing.PhysicalID
	if record.Stage == StageCapturePending && physicalID != "" {
		exporter, adoptErr := controller.adoptPending(context.Background(), physicalID)
		if adoptErr == nil {
			device := exporter.Device()
			completed, completeErr := controller.store.CompleteCapture(pair,
				record.CaptureGeneration, device, controller.now())
			if completeErr != nil {
				controller.hold(pair, record.CaptureGeneration, device, exporter)
				return completeErr
			}
			controller.hold(pair, completed.CaptureGeneration, completed.Device, exporter)
			return nil
		}
		if !errors.Is(adoptErr, rawusb.ErrCaptureNotPresent) {
			return adoptErr
		}
	}
	proof, err := controller.backend.ProveRawCapture(ctx, pair)
	if err != nil {
		return err
	}
	generation := existing.CaptureGeneration
	if generation == "" {
		generation, err = newGeneration()
		if err != nil {
			return err
		}
	}
	if proof.SessionGeneration == "" {
		proof.SessionGeneration = generation
	}
	record, err = controller.store.ArmCapture(proof, generation, controller.now())
	if err != nil {
		return err
	}
	physicalID, err = controller.backend.ReleaseATForRawCapture(ctx, proof)
	if err != nil {
		return err
	}
	exporter, err := controller.capture(context.Background(), physicalID)
	if err != nil {
		if captured, ok := rawusb.DeviceFromCaptureError(err); ok {
			if _, completeErr := controller.store.CompleteCapture(pair,
				record.CaptureGeneration, captured, controller.now()); completeErr != nil {
				return errors.Join(err, completeErr)
			}
		}
		return err
	}
	device := exporter.Device()
	completed, err := controller.store.CompleteCapture(pair, record.CaptureGeneration, device, controller.now())
	if err != nil {
		// Fail closed: keep the exporter and kernel capture alive even when the
		// state write failed. The pending debt remains for manual recovery.
		controller.hold(pair, record.CaptureGeneration, device, exporter)
		return err
	}
	controller.hold(pair, completed.CaptureGeneration, completed.Device, exporter)
	return nil
}

func (controller *Controller) adoptOne(ctx context.Context, record Record) error {
	controller.mu.Lock()
	if controller.exporters[record.Pair.EquipmentID] != nil {
		controller.mu.Unlock()
		return nil
	}
	controller.mu.Unlock()
	exporter, err := controller.adopt(context.Background(), record.Device)
	if err != nil {
		// A reboot or physical replug can legitimately remove the old kernel
		// capture and change BusID. Never follow the stored USB fields to a new
		// device: only a fresh unique equipment+ICCID proof may recapture it.
		proof, proofErr := controller.backend.ProveRawCapture(ctx, record.Pair)
		if proofErr != nil {
			return errors.Join(err, proofErr)
		}
		generation, generationErr := newGeneration()
		if generationErr != nil {
			return errors.Join(err, generationErr)
		}
		if proof.SessionGeneration == "" {
			proof.SessionGeneration = generation
		}
		if _, rearmErr := controller.store.RearmCapture(proof, generation, controller.now()); rearmErr != nil {
			return errors.Join(err, rearmErr)
		}
		return controller.captureOne(ctx, record.Pair, Record{CaptureGeneration: generation})
	}
	actual := exporter.Device()
	if actual != record.Device {
		if !rawusb.SamePersistentDevice(record.Device, actual) {
			return errors.New("persisted raw USB identity changed during adoption")
		}
		refreshed, refreshErr := controller.store.RefreshCaptureDevice(record.Pair,
			record.CaptureGeneration, actual, controller.now())
		if refreshErr != nil {
			controller.hold(record.Pair, record.CaptureGeneration, actual, exporter)
			return refreshErr
		}
		record = refreshed
	}
	controller.hold(record.Pair, record.CaptureGeneration, record.Device, exporter)
	return nil
}

func (controller *Controller) releaseOne(ctx context.Context, record Record) error {
	if record.Stage != StageReleasePending {
		if err := controller.store.SetAdapted(record.Pair, controller.now()); err != nil {
			return err
		}
		record.Stage = StageReleasePending
	}
	controller.mu.Lock()
	held := controller.exporters[record.Pair.EquipmentID]
	if held != nil && held.borrowed {
		controller.mu.Unlock()
		return errors.New("raw USB transport must stop before local adapted release")
	}
	if held != nil {
		delete(controller.exporters, record.Pair.EquipmentID)
	}
	controller.mu.Unlock()
	var releaseErr error
	if held != nil {
		releaseErr = held.exporter.Close()
	} else if record.Device.BusID != "" {
		releaseErr = controller.release(ctx, record.Device)
	}
	if err := controller.backend.VerifyAdapted(ctx, record.Pair); err != nil {
		return errors.Join(releaseErr, err)
	}
	return controller.store.ClearReleased(record.Pair, record.CaptureGeneration)
}

func (controller *Controller) hold(pair Pair, generation string, device rawusb.Device, exporter agentrawusb.Exporter) {
	controller.mu.Lock()
	controller.exporters[pair.EquipmentID] = &heldCapture{
		pair: pair, generation: generation, device: device, exporter: exporter,
	}
	controller.mu.Unlock()
}

// The following methods implement agentrawusb.SourceBackend. They borrow the
// local capture for one WSS session; remote stop can never close or unbind it.
func (controller *Controller) AcquireRawUSB(_ context.Context, target agentrawusb.SourceTarget) (agentrawusb.SourceClaim, error) {
	record, err := controller.recordForTarget(target)
	if err != nil {
		return agentrawusb.SourceClaim{}, err
	}
	device := record.Device
	return agentrawusb.SourceClaim{Device: &device}, nil
}

func (controller *Controller) RecordRawUSBDevice(target agentrawusb.SourceTarget, device rawusb.Device) error {
	record, err := controller.recordForTarget(target)
	if err != nil {
		return err
	}
	if record.Device != device {
		return errors.New("borrowed raw USB device identity changed")
	}
	return nil
}

func (controller *Controller) TakeRawUSB(target agentrawusb.SourceTarget) (agentrawusb.Exporter, bool, error) {
	if _, err := controller.recordForTarget(target); err != nil {
		return nil, false, err
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	held := controller.exporters[target.EquipmentID]
	if held == nil || held.exporter == nil {
		return nil, false, errors.New("local raw capture is not adopted")
	}
	if held.borrowed || held.generation != target.CaptureGeneration || held.pair != (Pair{EquipmentID: target.EquipmentID, CardID: target.CardID}) {
		return nil, false, errors.New("local raw capture is already borrowed or changed")
	}
	held.borrowed = true
	return held.exporter, true, nil
}

func (controller *Controller) PreserveRawUSB(target agentrawusb.SourceTarget, exporter agentrawusb.Exporter) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	held := controller.exporters[target.EquipmentID]
	if held == nil || held.exporter != exporter || held.generation != target.CaptureGeneration {
		return errors.New("local raw capture borrow identity changed")
	}
	held.borrowed = false
	controller.signal()
	return nil
}

func (controller *Controller) ReleaseRawUSB(agentrawusb.SourceTarget) error { return nil }

func (controller *Controller) RecoveryRawUSB() []agentlink.RawUSBRecoveryFact {
	snapshot, err := controller.store.Snapshot()
	if err != nil {
		return nil
	}
	result := make([]agentlink.RawUSBRecoveryFact, 0, len(snapshot.Captures))
	for _, record := range snapshot.Captures {
		if record.Stage != StageReserved {
			continue
		}
		result = append(result, agentlink.RawUSBRecoveryFact{
			AttachmentID: record.AttachmentID, SessionGeneration: record.SessionGeneration,
			EquipmentID: record.Pair.EquipmentID, CardID: record.Pair.CardID,
			USBSessionID:      "capture-" + record.CaptureGeneration,
			CaptureGeneration: record.CaptureGeneration,
			Device: agentlink.RawUSBDevice{
				BusID: record.Device.BusID, VendorID: record.Device.VendorID,
				ProductID: record.Device.ProductID, Serial: record.Device.Serial,
			},
			State: "capture_reserved",
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EquipmentID < result[j].EquipmentID })
	return result
}

func (controller *Controller) Topology(base agentlink.TopologySnapshot) agentlink.TopologySnapshot {
	base.RawUSBSource = true
	base.RawUSBRecoveries = controller.RecoveryRawUSB()
	return agentlink.NormalizeTopology(base)
}

func (controller *Controller) recordForTarget(target agentrawusb.SourceTarget) (Record, error) {
	snapshot, err := controller.store.Snapshot()
	if err != nil {
		return Record{}, err
	}
	pair := Pair{EquipmentID: target.EquipmentID, CardID: target.CardID}
	desired := false
	for _, current := range snapshot.Desired {
		if current == pair {
			desired = true
		}
	}
	if !desired {
		return Record{}, errors.New("local Agent mode is not raw for this modem and ICCID")
	}
	for _, record := range snapshot.Captures {
		if record.Pair == pair && record.Stage == StageReserved && record.CaptureGeneration == target.CaptureGeneration {
			return record, nil
		}
	}
	return Record{}, errors.New("local raw capture is not reserved")
}

func (controller *Controller) retryReady(pair Pair) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return !controller.now().Before(controller.retries[pair.key()].next)
}

func (controller *Controller) failed(pair Pair) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	state := controller.retries[pair.key()]
	state.failures++
	delay := time.Second
	for attempt := uint32(1); attempt < state.failures && delay < time.Minute; attempt++ {
		delay *= 2
	}
	if delay > time.Minute {
		delay = time.Minute
	}
	state.next = controller.now().Add(delay)
	controller.retries[pair.key()] = state
}

func (controller *Controller) succeeded(pair Pair) {
	controller.mu.Lock()
	delete(controller.retries, pair.key())
	controller.mu.Unlock()
}

func newGeneration() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "capture-" + hex.EncodeToString(value[:]), nil
}

var _ agentrawusb.SourceBackend = (*Controller)(nil)
