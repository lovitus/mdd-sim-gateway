//go:build windows && (amd64 || arm64)

// Package windowsmbn implements read-only Windows Mobile Broadband facts.
package windowsmbn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	win32 "github.com/deploymenttheory/go-bindings-win32/bindings/runtime/win32"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/foundation"
	mbn "github.com/deploymenttheory/go-bindings-win32/bindings/win32/networkmanagement/mobilebroadband"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/com"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/ole"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowsat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowsdataguard"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowspcm"
)

// Microsoft does not currently publish coclass IDs through win32metadata.
// This is the Windows SDK MbnInterfaceManager CLSID used by mbnapi.tlb.
var clsidMbnInterfaceManager = win32.GUID{
	Data1: 0xbdfee05b, Data2: 0x4418, Data3: 0x11dd,
	Data4: [8]byte{0x90, 0xed, 0x00, 0x1c, 0x25, 0x7c, 0xcf, 0xf1},
}

type Prober struct {
	mu                 sync.Mutex
	at                 *agentat.Manager
	guard              *windowsdataguard.Guard
	data               map[string]*dataBorrow
	raw                map[string]rawClaim
	localCapture       map[string]bool
	rawRecovery        *agentrawusb.RecoveryStore
	rawRecoveryOnly    bool
	sourceAgentID      string
	recovery           map[string]rawRecoveryAttempt
	restartPending     bool
	sessions           *agentmodem.SIMInsertionTracker
	rawProbe           func(context.Context) ([]agentmodem.Fact, error)
	rawCallStatus      func(context.Context, string) (agentat.CallState, error)
	freshSIMPINStatus  func(context.Context, string) (agentat.SIMPINStatus, error)
	rawVerifiedHangup  func(context.Context, string) (agentat.CallState, error)
	rawReleaseAT       func(string) (string, error)
	releaseCapturedRaw func(context.Context, rawusb.Device) error
}

type rawClaim struct {
	target         agentrawusb.SourceTarget
	claim          agentrawusb.SourceClaim
	exporter       agentrawusb.Exporter
	transportOwned bool
}

type rawRecoveryAttempt struct {
	failures uint32
	next     time.Time
}

func NewProber(simAPDU, protectData bool, sourceAgentID string,
	rawRecovery *agentrawusb.RecoveryStore, recoveryOnly bool) (*Prober, error) {
	if protectData && (strings.TrimSpace(sourceAgentID) == "" || rawRecovery == nil) {
		return nil, errors.New("managed Windows modem runtime requires raw handoff recovery state")
	}
	manager, err := windowsat.NewManager(simAPDU)
	if err != nil {
		return nil, err
	}
	var guard *windowsdataguard.Guard
	if protectData {
		guard, err = windowsdataguard.New()
		if err != nil {
			_ = manager.Close()
			return nil, fmt.Errorf("install persistent cellular data guard: %w", err)
		}
	}
	sessions, err := agentmodem.NewSIMInsertionTracker()
	if err != nil {
		_ = manager.Close()
		if guard != nil {
			_ = guard.Close()
		}
		return nil, fmt.Errorf("initialize modem SIM insertion tracker: %w", err)
	}
	prober := &Prober{
		at: manager, guard: guard, data: map[string]*dataBorrow{}, raw: map[string]rawClaim{},
		localCapture: map[string]bool{},
		rawRecovery:  rawRecovery, rawRecoveryOnly: recoveryOnly, sourceAgentID: strings.TrimSpace(sourceAgentID),
		recovery: map[string]rawRecoveryAttempt{}, sessions: sessions,
	}
	prober.rawProbe = func(ctx context.Context) ([]agentmodem.Fact, error) { return prober.probeLocked(ctx) }
	prober.rawCallStatus = manager.CallStatus
	prober.freshSIMPINStatus = manager.SIMPINStatusFresh
	prober.rawVerifiedHangup = manager.VerifiedHangup
	prober.rawReleaseAT = manager.ReleaseForRawUSB
	prober.releaseCapturedRaw = rawusb.RecoverCapturedDevice
	return prober, nil
}

// Probe executes in one COM apartment and releases every COM/BSTR/SAFEARRAY
// allocation before returning. A durable raw-handoff debt is the sole
// exception to read-only observation: after the USB parent returns, Probe
// reacquires the exact modem/card and runs VerifiedHangup before publishing it.
func (prober *Prober) Probe(ctx context.Context) ([]agentmodem.Fact, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return prober.probeLocked(ctx)
}

// ProbeSIMPINStatus is a local diagnostic. It performs the normal read-only
// probe and then reads CPIN, QCCID and the Quectel SC retry counters for every
// uniquely owned modem. It never submits a credential.
func (prober *Prober) ProbeSIMPINStatus(ctx context.Context) ([]agentmodem.Fact, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx)
	if err != nil {
		return nil, err
	}
	for index := range facts {
		if facts[index].AT.State != agentmodem.ATControlReady {
			continue
		}
		status, statusErr := prober.at.SIMPINStatusFull(ctx, facts[index].EquipmentID)
		if statusErr != nil {
			facts[index].SIM.PINRecovery = "status_unavailable"
			continue
		}
		facts[index].SIM.ICCID = status.CardID
		facts[index].SIM.PINState = string(status.State)
		facts[index].SIM.PINAttempts = cloneUint32(status.AttemptsRemaining)
	}
	return facts, nil
}

func (prober *Prober) probeLocked(ctx context.Context) (facts []agentmodem.Fact, err error) {
	defer func() {
		if err != nil {
			prober.sessions.Invalidate()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := com.CoInitializeEx(uint32(com.COINIT_MULTITHREADED)); err != nil {
		return nil, fmt.Errorf("initialize Windows MBN COM apartment: %w", err)
	}
	defer com.CoUninitialize()

	var root *win32.IUnknown
	if err := com.CoCreateInstance(
		&clsidMbnInterfaceManager, nil, com.CLSCTX_INPROC_SERVER,
		&mbn.IID_IMbnInterfaceManager, &root,
	); err != nil {
		if root != nil {
			root.Release()
		}
		return nil, fmt.Errorf("create Windows MBN interface manager: %w", err)
	}
	manager := win32.Cast[mbn.IMbnInterfaceManager](root)
	defer manager.Release()

	var interfaces *com.SAFEARRAY
	if err := manager.GetInterfaces(&interfaces); err != nil {
		if interfaces != nil {
			ole.SafeArrayDestroy(interfaces)
		}
		// Windows 7-11 return HRESULT_FROM_WIN32(ERROR_NOT_FOUND) when no
		// Mobile Broadband interface exists, despite the older API table not
		// documenting that result. No attachment is a successful empty probe.
		if errors.Is(err, syscall.Errno(foundation.ERROR_NOT_FOUND)) {
			return prober.finalizeFacts(ctx, nil)
		}
		return nil, fmt.Errorf("enumerate Windows MBN interfaces: %w", err)
	}
	if interfaces == nil {
		return prober.finalizeFacts(ctx, nil)
	}
	defer ole.SafeArrayDestroy(interfaces)

	lower, upper, err := bounds(interfaces)
	if err != nil {
		return nil, err
	}
	facts = make([]agentmodem.Fact, 0, max(0, int(upper-lower+1)))
	for index := lower; index <= upper; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var current *mbn.IMbnInterface
		if err := ole.SafeArrayGetElement(interfaces, &index, unsafe.Pointer(&current)); err != nil {
			if current != nil {
				current.Release()
			}
			return nil, fmt.Errorf("read Windows MBN interface array: %w", err)
		}
		if current == nil {
			continue
		}
		fact := probeInterface(current, index)
		current.Release()
		facts = append(facts, fact)
	}
	return prober.finalizeFacts(ctx, facts)
}

func (prober *Prober) finalizeFacts(ctx context.Context, observed []agentmodem.Fact) ([]agentmodem.Fact, error) {
	records := []agentrawusb.RecoveryRecord{}
	if prober.rawRecovery != nil {
		var err error
		records, err = prober.rawRecovery.Records()
		if err != nil {
			return nil, fmt.Errorf("read raw modem recovery state: %w", err)
		}
	}
	prober.protectData(ctx, observed)
	managed := make([]agentmodem.Fact, 0, len(observed))
	for _, fact := range observed {
		_, locallyCaptured := prober.localCapture[fact.EquipmentID]
		if _, exported := prober.raw[fact.EquipmentID]; !exported && !locallyCaptured {
			managed = append(managed, fact)
		}
	}
	prober.reconcileAT(ctx, managed)
	for index := range managed {
		if prober.data[managed[index].EquipmentID] != nil {
			managed[index].AT = agentmodem.ATControlFact{State: agentmodem.ATControlUnavailable,
				Detail: "protected cellular data owns modem operations"}
			managed[index].Capabilities.SMSReceive, managed[index].Capabilities.SMSSend = false, false
		}
	}
	pending := make(map[string]struct{}, len(records))
	for _, record := range records {
		pending[record.EquipmentID] = struct{}{}
		prober.recoverRawHandoff(ctx, managed, record)
	}
	facts := make([]agentmodem.Fact, 0, len(managed))
	for _, fact := range managed {
		if _, recovering := pending[fact.EquipmentID]; recovering {
			continue
		}
		if prober.rawRecoveryOnly {
			continue
		}
		facts = append(facts, fact)
	}
	sort.Slice(facts, func(left, right int) bool { return facts[left].AttachmentID < facts[right].AttachmentID })
	return prober.sessions.Observe(facts), nil
}

func (prober *Prober) Close() error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	var errs []error
	for _, current := range prober.data {
		errs = append(errs, current.borrow.Close())
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		errs = append(errs, disconnectData(ctx, current.target.AttachmentID))
		cancel()
	}
	prober.data = map[string]*dataBorrow{}
	prober.raw = map[string]rawClaim{}
	if prober.at != nil {
		errs = append(errs, prober.at.Close())
	}
	if prober.guard != nil {
		errs = append(errs, prober.guard.Close())
	}
	if prober.rawRecovery != nil {
		errs = append(errs, prober.rawRecovery.Close())
	}
	return errors.Join(errs...)
}

func (prober *Prober) recoverRawHandoff(ctx context.Context, facts []agentmodem.Fact,
	record agentrawusb.RecoveryRecord) {
	if current := prober.raw[record.EquipmentID]; prober.rawRecovery == nil ||
		current.claim.PhysicalID != "" || current.claim.Device != nil {
		return
	}
	if !record.ReleaseRequested {
		return
	}
	if record.SourceAgentID != prober.sourceAgentID {
		log.Printf("mdd-agent: raw modem recovery blocked: configured Agent ID does not own equipment %s", record.EquipmentID)
		return
	}
	now := time.Now().UTC()
	if attempt := prober.recovery[record.EquipmentID]; now.Before(attempt.next) {
		return
	}
	matches := make([]agentmodem.Fact, 0, 1)
	for _, fact := range facts {
		if fact.EquipmentID == record.EquipmentID {
			matches = append(matches, fact)
		}
	}
	if len(matches) != 1 {
		if record.USB != nil && prober.releaseCapturedRaw != nil {
			identity := rawusb.Device{
				StableID: record.USB.StableID, BusID: record.USB.BusID,
				VendorID: record.USB.VendorID, ProductID: record.USB.ProductID, Serial: record.USB.Serial,
			}
			if err := prober.releaseCapturedRaw(ctx, identity); err == nil {
				prober.failRawRecovery(record.EquipmentID, now, "captured USB parent was released; re-probing exact modem identity")
				return
			}
		}
		prober.failRawRecovery(record.EquipmentID, now, "exact modem is absent")
		return
	}
	if matches[0].AT.State != agentmodem.ATControlReady ||
		matches[0].Network.Guard.State != agentmodem.DataGuardProtected {
		prober.failRawRecovery(record.EquipmentID, now, "exact modem is absent or its AT/data guard is not ready")
		return
	}
	status, err := prober.freshSIMPINStatus(ctx, record.EquipmentID)
	if err != nil || status.CardID != record.CardID {
		prober.failRawRecovery(record.EquipmentID, now, "fresh SIM identity is unavailable or differs")
		return
	}
	call, err := prober.rawVerifiedHangup(ctx, record.EquipmentID)
	if err != nil || call.State != "idle" || !call.Authoritative || !call.TerminalConfirmed {
		prober.failRawRecovery(record.EquipmentID, now, "terminal physical hangup is not confirmed")
		return
	}
	if err := prober.rawRecovery.ClearExpected(record); err != nil {
		prober.failRawRecovery(record.EquipmentID, now, "durable handoff debt could not be cleared")
		return
	}
	delete(prober.recovery, record.EquipmentID)
}

func (prober *Prober) failRawRecovery(equipmentID string, now time.Time, detail string) {
	attempt := prober.recovery[equipmentID]
	attempt.failures++
	delay := time.Second
	for count := uint32(1); count < attempt.failures && delay < time.Minute; count++ {
		delay *= 2
		if delay > time.Minute {
			delay = time.Minute
		}
	}
	attempt.next = now.Add(delay)
	prober.recovery[equipmentID] = attempt
	log.Printf("mdd-agent: raw modem recovery pending for equipment %s: %s; retrying in %s", equipmentID, detail, delay)
}

func (prober *Prober) protectData(ctx context.Context, facts []agentmodem.Fact) {
	for index := range facts {
		facts[index].Network.Guard.State = agentmodem.DataGuardUnmanaged
		if prober.guard == nil || facts[index].AttachmentID == "" {
			continue
		}
		if err := prober.guard.Protect(ctx, facts[index].AttachmentID); err != nil {
			facts[index].Network.Guard = agentmodem.DataGuardFact{State: agentmodem.DataGuardFailed, Detail: bounded(err.Error())}
			facts[index].Condition = agentmodem.DeviceDegraded
			detail := "data_guard: " + err.Error()
			if facts[index].Detail != "" {
				detail = facts[index].Detail + "; " + detail
			}
			facts[index].Detail = bounded(detail)
			continue
		}
		facts[index].Network.Guard.State = agentmodem.DataGuardProtected
	}
}

func (prober *Prober) Operate(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if prober.data[operation.EquipmentID] != nil && operation.Action != agentmodem.OperationCallStatus &&
		operation.Action != agentmodem.OperationCallHangup {
		return agentmodem.OperationResult{}, fmt.Errorf("%w: protected cellular data owns modem operations", agentmodem.ErrOperationUnavailable)
	}
	facts, err := prober.probeLocked(ctx)
	if err != nil {
		return agentmodem.OperationResult{}, err
	}
	if err := agentmodem.ValidateOperationTarget(facts, operation); err != nil {
		return agentmodem.OperationResult{}, err
	}
	if operation.Action == agentmodem.OperationSMSSend || operation.Action == agentmodem.OperationSMSDelete ||
		operation.Action == agentmodem.OperationCallDial ||
		operation.Action == agentmodem.OperationCallAnswer || operation.Action == agentmodem.OperationCallReject ||
		operation.Action == agentmodem.OperationCallDTMF {
		if err := prober.requireFreshReadyCard(ctx, operation.EquipmentID, operation.CardID); err != nil {
			return agentmodem.OperationResult{}, err
		}
	}
	if operation.Action == agentmodem.OperationSMSList {
		messages, err := prober.at.ListSMS(ctx, operation.EquipmentID)
		if err != nil {
			return agentmodem.OperationResult{}, err
		}
		result := make([]agentmodem.SMSMessage, 0, len(messages))
		for _, message := range messages {
			result = append(result, agentmodem.SMSMessage{
				Index: message.Index, Indices: append([]int(nil), message.Indices...), State: message.State, Direction: message.Direction,
				Peer: message.Peer, Body: message.Body, ObservedAt: message.ObservedAt,
				Fingerprint: message.Fingerprint, Reference: message.Reference, Delivery: message.DeliveryState,
			})
		}
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "listed", Messages: result}}, nil
	}
	if operation.Action == agentmodem.OperationSMSSend {
		references, err := prober.at.SendSMS(ctx, operation.EquipmentID, operation.Number, operation.Body)
		if err != nil {
			return agentmodem.OperationResult{SMS: agentmodem.SMSResult{References: references}}, err
		}
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "submitted", References: references}}, nil
	}
	if operation.Action == agentmodem.OperationSMSDelete {
		if err := prober.at.DeleteSMS(ctx, operation.EquipmentID, operation.SMSIndices, operation.SMSFingerprint); err != nil {
			if errors.Is(err, agentat.ErrSMSIdentityChanged) {
				return agentmodem.OperationResult{}, agentmodem.ErrSMSStorageChanged
			}
			return agentmodem.OperationResult{}, err
		}
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "deleted"}}, nil
	}
	var call agentat.CallState
	switch operation.Action {
	case agentmodem.OperationCallStatus:
		call, err = prober.at.CallStatus(ctx, operation.EquipmentID)
	case agentmodem.OperationCallHangup:
		call, err = prober.at.VerifiedHangup(ctx, operation.EquipmentID)
	case agentmodem.OperationCallDial:
		call, err = prober.at.Dial(ctx, operation.EquipmentID, operation.Number)
	case agentmodem.OperationCallAnswer:
		call, err = prober.at.AnswerIncoming(ctx, operation.EquipmentID, agentat.IncomingCallFence{
			NativeIndex: operation.NativeCallIndex, Number: operation.Number,
		})
	case agentmodem.OperationCallReject:
		call, err = prober.at.RejectIncoming(ctx, operation.EquipmentID, agentat.IncomingCallFence{
			NativeIndex: operation.NativeCallIndex, Number: operation.Number,
		})
	case agentmodem.OperationCallDTMF:
		call, err = prober.at.SendDTMF(ctx, operation.EquipmentID, operation.Signal)
	default:
		return agentmodem.OperationResult{}, errors.New("unsupported modem operation")
	}
	if err != nil {
		if errors.Is(err, agentat.ErrIncomingCallChanged) {
			return agentmodem.OperationResult{}, agentmodem.ErrIncomingCallChanged
		}
		return agentmodem.OperationResult{}, err
	}
	return agentmodem.OperationResult{Call: agentmodem.CallResult{
		State: call.State, Direction: call.Direction, Number: call.Number,
		NativeIndex: call.NativeIndex, VoiceCalls: call.VoiceCalls, IncomingCalls: call.IncomingCalls,
		ObservedAt: call.ObservedAt, Authoritative: call.Authoritative,
		TerminalConfirmed: call.TerminalConfirmed, Strategy: call.Strategy,
	}}, nil
}

func (prober *Prober) AuthenticateSIMAKA(ctx context.Context, request agentmodem.SIMAKARequest) (agentmodem.SIMAKAResult, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx)
	if err != nil {
		return agentmodem.SIMAKAResult{}, err
	}
	if err := agentmodem.ValidateSIMAKATarget(facts, request); err != nil {
		return agentmodem.SIMAKAResult{}, err
	}
	call, err := prober.at.CallStatus(ctx, request.EquipmentID)
	if err != nil {
		return agentmodem.SIMAKAResult{}, err
	}
	if !call.Authoritative || call.State != "idle" {
		return agentmodem.SIMAKAResult{}, agentmodem.ErrOperationUnavailable
	}
	result, err := prober.at.AuthenticateAKA(ctx, request.EquipmentID, request.Application, request.RAND, request.AUTN)
	if err != nil {
		return agentmodem.SIMAKAResult{}, err
	}
	return agentmodem.SIMAKAResult{Body: result.Body, SW1: result.SW1, SW2: result.SW2}, nil
}

func (prober *Prober) EnterSIMPIN(ctx context.Context, request agentmodem.SIMPINRequest) (agentmodem.SIMPINResult, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx)
	if err != nil {
		return agentmodem.SIMPINResult{}, err
	}
	matches := 0
	var target agentmodem.Fact
	for _, fact := range facts {
		if fact.AttachmentID == request.AttachmentID && fact.EquipmentID == request.EquipmentID &&
			fact.SIM.ICCID == request.CardID {
			matches++
			target = fact
		}
	}
	if matches != 1 {
		return agentmodem.SIMPINResult{}, agentmodem.ErrOperationTargetReplaced
	}
	if target.SIM.State != agentmodem.SIMLocked || target.SIM.PINState != string(agentat.SIMPINRequired) ||
		target.AT.State != agentmodem.ATControlReady {
		return agentmodem.SIMPINResult{}, agentmodem.ErrOperationUnavailable
	}
	result, err := prober.at.EnterSIMPIN(ctx, request.EquipmentID, request.CardID, request.PIN)
	converted := agentmodem.SIMPINResult{
		Attempted: result.Attempted, Ready: result.Status.State == agentat.SIMPINNotRequired,
		AttemptsRemaining: cloneUint32(result.Status.AttemptsRemaining),
	}
	return converted, err
}

const (
	uacPCMWriteBatchBytes    = 320  // one 20 ms frame for the continuous audio helper
	serialPCMWriteBatchBytes = 1600 // Quectel host-to-modem serial packet, 100 ms
)

func (prober *Prober) OpenVoicePCM(ctx context.Context, target agentmodem.MediaTarget) (io.ReadWriteCloser, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx)
	if err != nil {
		return nil, err
	}
	if err := agentmodem.ValidateMediaTarget(facts, target); err != nil {
		return nil, err
	}
	if err := prober.requireFreshReadyCard(ctx, target.EquipmentID, target.CardID); err != nil {
		return nil, err
	}
	physicalID, err := prober.at.PhysicalID(target.EquipmentID)
	if err != nil {
		return nil, err
	}
	uac, uacErr := windowspcm.DiscoverUAC(ctx, physicalID)
	if uacErr == nil {
		if err := prober.at.EnableVoicePCMMode(ctx, target.EquipmentID, 2); err != nil {
			uacErr = fmt.Errorf("enable modem UAC mode: %w", err)
		} else if uacPCM, err := uac.Open(); err == nil {
			return &voicePCMEndpoint{ReadWriteCloser: uacPCM, writeBatchBytes: uacPCMWriteBatchBytes, prober: prober, equipmentID: target.EquipmentID}, nil
		} else {
			uacErr = fmt.Errorf("open matching modem UAC endpoint: %w", err)
			_ = prober.at.DisableVoicePCM(ctx, target.EquipmentID)
		}
	}
	serialPCM, err := windowspcm.Open(physicalID)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open matching modem PCM endpoint: %w", err), uacErr)
	}
	if err := prober.at.EnableVoicePCM(ctx, target.EquipmentID); err != nil {
		_ = serialPCM.Close()
		return nil, errors.Join(fmt.Errorf("enable modem PCM mode: %w", err), uacErr)
	}
	return &voicePCMEndpoint{ReadWriteCloser: serialPCM, writeBatchBytes: serialPCMWriteBatchBytes, prober: prober, equipmentID: target.EquipmentID}, nil
}

func (prober *Prober) requireFreshReadyCard(ctx context.Context, equipmentID, cardID string) error {
	if prober.freshSIMPINStatus == nil {
		return agentmodem.ErrOperationUnavailable
	}
	status, err := prober.freshSIMPINStatus(ctx, equipmentID)
	if err != nil {
		return err
	}
	if status.CardID != cardID || status.State != agentat.SIMPINNotRequired {
		return agentmodem.ErrOperationTargetReplaced
	}
	return nil
}

type voicePCMEndpoint struct {
	io.ReadWriteCloser
	writeBatchBytes int
	prober          *Prober
	equipmentID     string
	once            sync.Once
	err             error
}

func (endpoint *voicePCMEndpoint) PCMWriteBatchBytes() int { return endpoint.writeBatchBytes }

func (endpoint *voicePCMEndpoint) Close() error {
	endpoint.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		endpoint.prober.mu.Lock()
		disableErr := endpoint.prober.at.DisableVoicePCM(ctx, endpoint.equipmentID)
		endpoint.prober.mu.Unlock()
		cancel()
		endpoint.err = errors.Join(disableErr, endpoint.ReadWriteCloser.Close())
	})
	return endpoint.err
}

func (prober *Prober) reconcileAT(ctx context.Context, facts []agentmodem.Fact) {
	if prober.at == nil {
		for index := range facts {
			facts[index].AT = agentmodem.ATControlFact{State: agentmodem.ATControlUnknown}
		}
		return
	}
	targets := make([]agentat.Target, 0, len(facts))
	for _, fact := range facts {
		targets = append(targets, agentat.Target{AttachmentID: fact.AttachmentID, EquipmentID: fact.EquipmentID})
	}
	snapshots := prober.at.Reconcile(ctx, targets)
	for index := range facts {
		snapshot, exists := snapshots[facts[index].AttachmentID]
		if !exists {
			facts[index].AT = agentmodem.ATControlFact{State: agentmodem.ATControlUnknown}
			continue
		}
		facts[index].AT = agentmodem.ATControlFact{
			State: agentmodem.ATControlState(snapshot.State), Port: snapshot.Port, Detail: snapshot.Detail,
			CallSignalling: snapshot.CallSignalling, SMS: snapshot.SMS, SIMAPDU: snapshot.SIMAPDU,
			SIMAPDUOnDemand: snapshot.SIMAPDUOnDemand,
		}
		if snapshot.OwnerGeneration != 0 {
			facts[index].ContinuityEpoch = fmt.Sprintf("%s:at-owner:%d", facts[index].AttachmentID, snapshot.OwnerGeneration)
		}
		if facts[index].SIM.State == agentmodem.SIMReady {
			facts[index].SIM.PINState = string(agentat.SIMPINNotRequired)
		}
		if facts[index].SIM.State != agentmodem.SIMLocked || facts[index].AT.State != agentmodem.ATControlReady {
			prober.at.InvalidateSIMPINStatus(facts[index].EquipmentID)
			continue
		}
		status, err := prober.at.SIMPINStatus(ctx, facts[index].EquipmentID)
		if err != nil {
			facts[index].SIM.PINState = string(agentat.SIMPINUnknown)
			facts[index].SIM.PINRecovery = "status_unavailable"
			continue
		}
		facts[index].SIM.ICCID = status.CardID
		facts[index].SIM.PINState = string(status.State)
		facts[index].SIM.PINAttempts = cloneUint32(status.AttemptsRemaining)
	}
}

func cloneUint32(value *uint32) *uint32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func probeInterface(value *mbn.IMbnInterface, arrayIndex int32) agentmodem.Fact {
	fact := agentmodem.Fact{
		Condition: agentmodem.DeviceReady,
		SIM:       agentmodem.SIMFact{State: agentmodem.SIMUnknown},
		Network: agentmodem.NetworkFact{
			Registration:  agentmodem.RegistrationUnknown,
			SoftwareRadio: agentmodem.RadioUnknown, HardwareRadio: agentmodem.RadioUnknown,
			Data: agentmodem.DataUnknown, Guard: agentmodem.DataGuardFact{State: agentmodem.DataGuardUnmanaged},
		},
	}
	var failures []string
	continuityAuthoritative := true

	var attachment foundation.BSTR
	if err := value.Get_InterfaceID(&attachment); err != nil {
		failures = append(failures, "interface_id: "+err.Error())
		continuityAuthoritative = false
	} else {
		fact.AttachmentID = normalizeAttachmentID(takeBSTR(attachment))
	}

	var ready mbn.MBN_READY_STATE
	if err := value.GetReadyState(&ready); err != nil {
		failures = append(failures, "ready_state: "+err.Error())
		continuityAuthoritative = false
	} else {
		fact.SIM.State = simState(ready)
	}

	var caps mbn.MBN_INTERFACE_CAPS
	if err := value.GetInterfaceCapability(&caps); err != nil {
		failures = append(failures, "capabilities: "+err.Error())
		continuityAuthoritative = false
	} else {
		fact.EquipmentID = takeBSTR(caps.DeviceID)
		fact.Manufacturer = takeBSTR(caps.Manufacturer)
		fact.Model = takeBSTR(caps.Model)
		fact.Firmware = takeBSTR(caps.FirmwareInfo)
		takeBSTR(caps.CustomDataClass)
		takeBSTR(caps.CustomBandClass)
		fact.Capabilities = agentmodem.Capabilities{
			CellularData:  caps.DataClass != 0,
			SMSReceive:    caps.SmsCaps&uint32(mbn.MBN_SMS_CAPS_PDU_RECEIVE) != 0,
			SMSSend:       caps.SmsCaps&uint32(mbn.MBN_SMS_CAPS_PDU_SEND) != 0,
			MBNVoiceClass: voiceClass(caps.VoiceClass),
		}
	}

	if fact.SIM.State == agentmodem.SIMReady {
		subscriber, err := value.GetSubscriberInformation()
		if err != nil {
			if subscriber != nil {
				subscriber.Release()
			}
			failures = append(failures, "subscriber: "+err.Error())
			continuityAuthoritative = false
		} else if subscriber != nil {
			fact.SIM.ICCID = readBSTR(subscriber.Get_SimIccID)
			fact.SIM.IMSI = readBSTR(subscriber.Get_SubscriberID)
			if values, err := readBSTRArray(subscriber); err != nil {
				failures = append(failures, "telephone_numbers: "+err.Error())
			} else {
				fact.SIM.MSISDNs = values
			}
			subscriber.Release()
		} else {
			continuityAuthoritative = false
			failures = append(failures, "subscriber: empty subscriber information")
		}
	}

	probeNetwork(value, &fact, &failures)
	probeSMS(value, &fact)
	if fact.AttachmentID == "" {
		fact.AttachmentID = fmt.Sprintf("mbn-unidentified-%d", arrayIndex)
	}
	if continuityAuthoritative {
		fact.ContinuityEpoch = fact.AttachmentID
	}
	if len(failures) != 0 {
		fact.Condition = agentmodem.DeviceDegraded
		fact.Detail = bounded(strings.Join(failures, "; "))
	}
	return fact
}

func probeNetwork(value *mbn.IMbnInterface, fact *agentmodem.Fact, failures *[]string) {
	if registration, err := query[mbn.IMbnRegistration](value, &mbn.IID_IMbnRegistration); err == nil {
		var state mbn.MBN_REGISTER_STATE
		if err := registration.GetRegisterState(&state); err == nil {
			fact.Network.Registration = registrationState(state)
		} else {
			*failures = append(*failures, "registration: "+err.Error())
		}
		fact.Network.OperatorID = readBSTR(registration.GetProviderID)
		fact.Network.OperatorName = readBSTR(registration.GetProviderName)
		registration.Release()
	} else {
		*failures = append(*failures, "registration_interface: "+err.Error())
	}
	if signal, err := query[mbn.IMbnSignal](value, &mbn.IID_IMbnSignal); err == nil {
		var percent uint32
		if signal.GetSignalStrength(&percent) == nil && percent <= 100 {
			fact.Network.SignalPercent = &percent
		}
		signal.Release()
	}
	if radio, err := query[mbn.IMbnRadio](value, &mbn.IID_IMbnRadio); err == nil {
		var software, hardware mbn.MBN_RADIO
		if radio.Get_SoftwareRadioState(&software) == nil {
			fact.Network.SoftwareRadio = radioState(software)
		}
		if radio.Get_HardwareRadioState(&hardware) == nil {
			fact.Network.HardwareRadio = radioState(hardware)
		}
		radio.Release()
	}
	connection, err := value.GetConnection()
	if err == nil && connection != nil {
		var state mbn.MBN_ACTIVATION_STATE
		var profile foundation.BSTR
		if err := connection.GetConnectionState(&state, &profile); err == nil {
			fact.Network.Data = dataState(state)
			fact.Network.Profile = takeBSTR(profile)
		} else {
			*failures = append(*failures, "connection_state: "+err.Error())
		}
		connection.Release()
	} else if err != nil {
		if connection != nil {
			connection.Release()
		}
		if state, unavailable := dataStateFromConnectionError(err); unavailable {
			fact.Network.Data = state
		} else {
			*failures = append(*failures, "connection: "+err.Error())
		}
	}
}

func probeSMS(value *mbn.IMbnInterface, fact *agentmodem.Fact) {
	if !fact.Capabilities.SMSReceive && !fact.Capabilities.SMSSend {
		return
	}
	sms, err := query[mbn.IMbnSms](value, &mbn.IID_IMbnSms)
	if err != nil {
		fact.SIM.SMSError = bounded(err.Error())
		return
	}
	defer sms.Release()
	var configuration *mbn.IMbnSmsConfiguration
	err = sms.GetSmsConfiguration(&configuration)
	if err != nil || configuration == nil {
		if configuration != nil {
			configuration.Release()
		}
		if err != nil {
			fact.SIM.SMSError = bounded(err.Error())
		}
		return
	}
	defer configuration.Release()
	fact.SIM.Configured = true
	fact.SIM.SMSC = readBSTR(configuration.Get_ServiceCenterAddress)
}

func query[T any](source *mbn.IMbnInterface, iid *win32.GUID) (*T, error) {
	var unknown *win32.IUnknown
	if err := source.QueryInterface(iid, &unknown); err != nil {
		if unknown != nil {
			unknown.Release()
		}
		return nil, err
	}
	return win32.Cast[T](unknown), nil
}

func bounds(array *com.SAFEARRAY) (int32, int32, error) {
	var lower, upper int32
	if err := ole.SafeArrayGetLBound(array, 1, &lower); err != nil {
		return 0, 0, err
	}
	if err := ole.SafeArrayGetUBound(array, 1, &upper); err != nil {
		return 0, 0, err
	}
	return lower, upper, nil
}

func readBSTR(get func(*foundation.BSTR) error) string {
	var value foundation.BSTR
	if get(&value) != nil {
		return ""
	}
	return takeBSTR(value)
}

func takeBSTR(value foundation.BSTR) string {
	if value == nil {
		return ""
	}
	result := win32.UTF16ToString(value)
	foundation.SysFreeString(value)
	return result
}

func readBSTRArray(subscriber *mbn.IMbnSubscriberInformation) ([]string, error) {
	var array *com.SAFEARRAY
	if err := subscriber.Get_TelephoneNumbers(&array); err != nil {
		return nil, err
	}
	if array == nil {
		return []string{}, nil
	}
	defer ole.SafeArrayDestroy(array)
	lower, upper, err := bounds(array)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, max(0, int(upper-lower+1)))
	for index := lower; index <= upper; index++ {
		var value foundation.BSTR
		if err := ole.SafeArrayGetElement(array, &index, unsafe.Pointer(&value)); err != nil {
			return nil, err
		}
		if text := takeBSTR(value); text != "" {
			result = append(result, text)
		}
	}
	return result, nil
}

func simState(value mbn.MBN_READY_STATE) agentmodem.SIMState {
	switch value {
	case mbn.MBN_READY_STATE_INITIALIZED:
		return agentmodem.SIMReady
	case mbn.MBN_READY_STATE_SIM_NOT_INSERTED, mbn.MBN_READY_STATE_NO_ESIM_PROFILE:
		return agentmodem.SIMAbsent
	case mbn.MBN_READY_STATE_DEVICE_LOCKED, mbn.MBN_READY_STATE_DEVICE_BLOCKED:
		return agentmodem.SIMLocked
	case mbn.MBN_READY_STATE_BAD_SIM, mbn.MBN_READY_STATE_FAILURE:
		return agentmodem.SIMFailed
	default:
		return agentmodem.SIMUnknown
	}
}

func registrationState(value mbn.MBN_REGISTER_STATE) agentmodem.RegistrationState {
	switch value {
	case mbn.MBN_REGISTER_STATE_DEREGISTERED:
		return agentmodem.RegistrationUnregistered
	case mbn.MBN_REGISTER_STATE_SEARCHING:
		return agentmodem.RegistrationSearching
	case mbn.MBN_REGISTER_STATE_HOME:
		return agentmodem.RegistrationHome
	case mbn.MBN_REGISTER_STATE_ROAMING, mbn.MBN_REGISTER_STATE_PARTNER:
		return agentmodem.RegistrationRoaming
	case mbn.MBN_REGISTER_STATE_DENIED:
		return agentmodem.RegistrationDenied
	default:
		return agentmodem.RegistrationUnknown
	}
}

func radioState(value mbn.MBN_RADIO) agentmodem.RadioState {
	if value == mbn.MBN_RADIO_ON {
		return agentmodem.RadioOn
	}
	if value == mbn.MBN_RADIO_OFF {
		return agentmodem.RadioOff
	}
	return agentmodem.RadioUnknown
}

func dataState(value mbn.MBN_ACTIVATION_STATE) agentmodem.DataState {
	switch value {
	case mbn.MBN_ACTIVATION_STATE_ACTIVATED:
		return agentmodem.DataConnected
	case mbn.MBN_ACTIVATION_STATE_ACTIVATING:
		return agentmodem.DataConnecting
	case mbn.MBN_ACTIVATION_STATE_DEACTIVATED:
		return agentmodem.DataDisconnected
	case mbn.MBN_ACTIVATION_STATE_DEACTIVATING:
		return agentmodem.DataDisconnecting
	default:
		return agentmodem.DataUnknown
	}
}

func voiceClass(value mbn.MBN_VOICE_CLASS) string {
	switch value {
	case mbn.MBN_VOICE_CLASS_NO_VOICE:
		return "no_voice"
	case mbn.MBN_VOICE_CLASS_SEPARATE_VOICE_DATA:
		return "separate_voice_data"
	case mbn.MBN_VOICE_CLASS_SIMULTANEOUS_VOICE_DATA:
		return "simultaneous_voice_data"
	default:
		return "unknown"
	}
}

func normalizeAttachmentID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "{")
	value = strings.TrimSuffix(value, "}")
	var result strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			result.WriteRune(character)
		}
	}
	return strings.ToLower(result.String())
}

func bounded(value string) string {
	value = strings.ToValidUTF8(value, "?")
	if len(value) > 1024 {
		value = strings.ToValidUTF8(value[:1024], "?")
	}
	return value
}
