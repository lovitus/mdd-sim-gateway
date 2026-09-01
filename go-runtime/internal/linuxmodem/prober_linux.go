//go:build linux

// Package linuxmodem connects ModemManager's passive USB inventory to the
// platform-neutral exclusive AT owner. ModemManager is inhibited only after a
// fresh no-bearer check; MDD never asks it to create a cellular data bearer.
package linuxmodem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulario"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
)

type Prober struct {
	mu            sync.Mutex
	manager       modemManager
	at            *agentat.Manager
	audioHelper   string
	sysRoot       string
	simAPDU       bool
	devices       map[string]*ownedDevice
	guard         *linuxdataguard.Guard
	raw           map[string]rawClaim
	localCapture  map[string]bool
	data          map[string]*dataClaim
	rawRecovery   *agentrawusb.RecoveryStore
	recoveryOnly  bool
	sourceAgentID string
	recovery      map[string]rawRecoveryAttempt
}

type ownedDevice struct {
	snapshot       modemSnapshot
	usb            usbGeneration
	lastFact       agentmodem.Fact
	lastFactAt     time.Time
	probeError     string
	releasePending bool
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

func NewProber(simAPDU bool) (*Prober, error) {
	manager, err := openModemManager()
	if err != nil {
		return nil, err
	}
	audioHelper, err := cellulario.ResolveSibling("mdd-call-audio-helper")
	if err != nil {
		_ = manager.Close()
		return nil, err
	}
	return newProber(manager, simAPDU, audioHelper, "/sys", openLinuxAT)
}

func NewManagedProber(simAPDU bool, guard *linuxdataguard.Guard, sourceAgentID string,
	rawRecovery *agentrawusb.RecoveryStore, recoveryOnly bool) (*Prober, error) {
	if guard == nil || strings.TrimSpace(sourceAgentID) == "" || rawRecovery == nil {
		return nil, errors.New("managed Linux modem runtime requires persistent guard and raw recovery state")
	}
	prober, err := NewProber(simAPDU)
	if err != nil {
		return nil, err
	}
	prober.guard = guard
	prober.rawRecovery = rawRecovery
	prober.recoveryOnly = recoveryOnly
	prober.sourceAgentID = strings.TrimSpace(sourceAgentID)
	return prober, nil
}

func newProber(manager modemManager, simAPDU bool, audioHelper, sysRoot string, opener agentat.Opener) (*Prober, error) {
	if manager == nil || opener == nil || strings.TrimSpace(audioHelper) == "" || !strings.HasPrefix(sysRoot, "/") {
		return nil, errors.New("invalid Linux modem adapter configuration")
	}
	prober := &Prober{
		manager: manager, audioHelper: audioHelper, sysRoot: sysRoot, simAPDU: simAPDU,
		devices: make(map[string]*ownedDevice), raw: make(map[string]rawClaim),
		localCapture: make(map[string]bool), data: make(map[string]*dataClaim),
		recovery: make(map[string]rawRecoveryAttempt),
	}
	at, err := agentat.NewManagerWithSIMAPDU(prober.enumerateAT, opener, simAPDU)
	if err != nil {
		_ = manager.Close()
		return nil, err
	}
	prober.at = at
	return prober, nil
}

func (prober *Prober) Probe(ctx context.Context) ([]agentmodem.Fact, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return prober.probeLocked(ctx, false)
}

func (prober *Prober) ProbeSIMPINStatus(ctx context.Context) ([]agentmodem.Fact, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
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

func (prober *Prober) probeLocked(ctx context.Context, fresh bool) ([]agentmodem.Fact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prober.retryDataCleanup()
	inventory, inventoryErr := prober.manager.Inventory(ctx)
	blocked := make([]agentmodem.Fact, 0)
	if inventoryErr == nil {
		blocked = prober.acquire(ctx, inventory)
	} else if len(prober.devices) == 0 {
		return nil, inventoryErr
	}

	reset := make(map[string]struct{})
	releaseUIDs := make([]string, 0)
	for uid, current := range prober.devices {
		if _, exported := prober.raw[current.snapshot.EquipmentID]; exported {
			continue
		}
		if prober.data[current.snapshot.EquipmentID] != nil {
			continue
		}
		if current.releasePending {
			reset[current.snapshot.EquipmentID] = struct{}{}
			releaseUIDs = append(releaseUIDs, uid)
			continue
		}
		generation, err := resolveUSBGeneration(prober.sysRoot, current.snapshot.ATPorts)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				reset[current.snapshot.EquipmentID] = struct{}{}
				current.releasePending = true
				current.probeError = "USB attachment disappeared; releasing ModemManager ownership"
				releaseUIDs = append(releaseUIDs, uid)
				continue
			}
			current.probeError = bounded(err.Error(), 1024)
			continue
		}
		current.probeError = ""
		if generation.Generation != current.usb.Generation {
			// A USB generation is the ownership boundary. ModemManager is
			// inhibited while MDD owns the device, so it cannot publish a new
			// IMEI for a replacement that appeared at the same physical path.
			// Release the old owner and let the next inventory reacquire and
			// re-prove the equipment identity instead of carrying it forward.
			reset[current.snapshot.EquipmentID] = struct{}{}
			current.releasePending = true
			current.probeError = "USB generation changed; re-proving modem equipment identity"
			releaseUIDs = append(releaseUIDs, uid)
		}
	}
	if len(reset) != 0 {
		prober.at.Reconcile(ctx, prober.targetsExcept(reset))
	}
	for _, uid := range releaseUIDs {
		current := prober.devices[uid]
		if current == nil {
			continue
		}
		if err := prober.manager.Inhibit(ctx, uid, false); err != nil {
			current.probeError = bounded("release ModemManager ownership: "+err.Error(), 1024)
			continue
		}
		delete(prober.devices, uid)
	}

	snapshots := prober.at.Reconcile(ctx, prober.targetsExcept(nil))
	facts := make([]agentmodem.Fact, 0, len(prober.devices)+len(blocked))
	for _, current := range prober.devices {
		if _, exported := prober.raw[current.snapshot.EquipmentID]; exported {
			continue
		}
		if claim := prober.data[current.snapshot.EquipmentID]; claim != nil {
			facts = append(facts, prober.dataFact(current, claim))
			continue
		}
		fact, err := prober.fact(ctx, current, snapshots[current.usb.AttachmentID], fresh)
		if err != nil {
			fact.Condition = agentmodem.DeviceDegraded
			fact.Detail = bounded(err.Error(), 1024)
		}
		if inventoryErr != nil {
			fact.Condition = agentmodem.DeviceDegraded
			fact.Detail = bounded("ModemManager inventory unavailable: "+inventoryErr.Error(), 1024)
		}
		facts = append(facts, fact)
	}
	facts = append(facts, blocked...)
	records := []agentrawusb.RecoveryRecord{}
	if prober.rawRecovery != nil {
		var err error
		records, err = prober.rawRecovery.Records()
		if err != nil {
			return nil, fmt.Errorf("read raw modem recovery state: %w", err)
		}
	}
	pending := make(map[string]struct{}, len(records))
	for _, record := range records {
		pending[record.EquipmentID] = struct{}{}
		prober.recoverRawHandoff(ctx, facts, record)
	}
	visible := facts[:0]
	for _, fact := range facts {
		_, locallyCaptured := prober.localCapture[fact.EquipmentID]
		if _, recovering := pending[fact.EquipmentID]; recovering || locallyCaptured || prober.recoveryOnly {
			continue
		}
		visible = append(visible, fact)
	}
	facts = visible
	sort.Slice(facts, func(left, right int) bool { return facts[left].AttachmentID < facts[right].AttachmentID })
	return facts, nil
}

func (prober *Prober) acquire(ctx context.Context, inventory []modemSnapshot) []agentmodem.Fact {
	blocked := make([]agentmodem.Fact, 0)
	equipmentOwners := make(map[string]string, len(prober.devices)+len(inventory))
	for uid, current := range prober.devices {
		equipmentOwners[current.snapshot.EquipmentID] = uid
	}
	duplicates := make(map[string]struct{})
	for _, snapshot := range inventory {
		if owner, exists := equipmentOwners[snapshot.EquipmentID]; exists && owner != snapshot.UID {
			duplicates[snapshot.EquipmentID] = struct{}{}
		} else {
			equipmentOwners[snapshot.EquipmentID] = snapshot.UID
		}
	}
	for _, snapshot := range inventory {
		if prober.devices[snapshot.UID] != nil {
			continue
		}
		generation, err := resolveUSBGeneration(prober.sysRoot, snapshot.ATPorts)
		if err != nil {
			blocked = append(blocked, unavailableFact(snapshot, usbGeneration{}, "exact USB/AT ownership is unavailable: "+err.Error(), false))
			continue
		}
		if prober.guard != nil {
			if guardErr := prober.guard.VerifyProtected(ctx, generation.PhysicalID, snapshot.NetPorts); guardErr != nil {
				blocked = append(blocked, unavailableFact(snapshot, generation, "persistent cellular data guard is unavailable: "+guardErr.Error(), snapshot.Connected))
				continue
			}
		}
		switch {
		case snapshot.Connected:
			if prober.guard != nil && len(snapshot.Bearers) != 0 {
				var failures []error
				for _, bearer := range snapshot.Bearers {
					failures = append(failures, prober.manager.Disconnect(ctx, bearer))
				}
				if cleanupErr := errors.Join(failures...); cleanupErr != nil {
					blocked = append(blocked, unavailableFact(snapshot, generation,
						"stale cellular bearer cleanup failed: "+cleanupErr.Error(), true))
				} else {
					blocked = append(blocked, unavailableFact(snapshot, generation,
						"stale cellular bearer was disconnected; re-probing ownership", false))
				}
				continue
			}
			blocked = append(blocked, unavailableFact(snapshot, generation, "an existing cellular data bearer prevents ownership handoff", true))
		case hasKey(duplicates, snapshot.EquipmentID):
			blocked = append(blocked, unavailableFact(snapshot, generation, "multiple physical modems reported the same equipment identity", false))
		default:
			if err := prober.manager.Inhibit(ctx, snapshot.UID, true); err != nil {
				blocked = append(blocked, unavailableFact(snapshot, generation, "ModemManager ownership handoff failed: "+err.Error(), false))
				continue
			}
			prober.devices[snapshot.UID] = &ownedDevice{snapshot: snapshot, usb: generation}
		}
	}
	return blocked
}

func (prober *Prober) enumerateAT() ([]agentat.Candidate, error) {
	result := make([]agentat.Candidate, 0)
	for _, current := range prober.devices {
		if current.releasePending {
			continue
		}
		if _, exported := prober.raw[current.snapshot.EquipmentID]; exported {
			continue
		}
		if prober.data[current.snapshot.EquipmentID] != nil {
			continue
		}
		result = append(result, linuxATCandidates(current.snapshot, current.usb.PhysicalID)...)
	}
	return result, nil
}

func (prober *Prober) targetsExcept(excluded map[string]struct{}) []agentat.Target {
	result := make([]agentat.Target, 0, len(prober.devices))
	for _, current := range prober.devices {
		if current.releasePending {
			continue
		}
		if _, exported := prober.raw[current.snapshot.EquipmentID]; exported {
			continue
		}
		if _, skip := excluded[current.snapshot.EquipmentID]; skip {
			continue
		}
		result = append(result, agentat.Target{
			AttachmentID: current.usb.AttachmentID, EquipmentID: current.snapshot.EquipmentID,
		})
	}
	return result
}

func (prober *Prober) fact(ctx context.Context, current *ownedDevice, at agentat.Snapshot, fresh bool) (agentmodem.Fact, error) {
	now := time.Now()
	if !fresh && !current.lastFactAt.IsZero() && now.Sub(current.lastFactAt) < 5*time.Second && current.probeError == "" {
		return cloneFact(current.lastFact), nil
	}
	fact := agentmodem.Fact{
		AttachmentID: current.usb.AttachmentID, EquipmentID: current.snapshot.EquipmentID,
		Manufacturer: current.snapshot.Manufacturer, Model: current.snapshot.Model, Firmware: current.snapshot.Firmware,
		Condition: agentmodem.DeviceReady,
		AT: agentmodem.ATControlFact{
			State: agentmodem.ATControlState(at.State), Port: at.Port, Detail: at.Detail,
			CallSignalling: at.CallSignalling, SMS: at.SMS, SIMAPDU: at.SIMAPDU,
		},
		SIM: agentmodem.SIMFact{State: agentmodem.SIMUnknown},
		Network: agentmodem.NetworkFact{
			Registration: agentmodem.RegistrationUnknown, SoftwareRadio: agentmodem.RadioUnknown,
			HardwareRadio: agentmodem.RadioUnknown, Data: agentmodem.DataDisconnected,
			Guard: agentmodem.DataGuardFact{State: agentmodem.DataGuardUnmanaged},
		},
	}
	if current.probeError != "" {
		fact.AT = agentmodem.ATControlFact{State: agentmodem.ATControlDegraded, Detail: current.probeError}
		return fact, errors.New(current.probeError)
	}
	if fact.AT.State != agentmodem.ATControlReady {
		if fact.AT.State == "" || fact.AT.State == agentmodem.ATControlUnknown {
			fact.AT.State = agentmodem.ATControlUnavailable
		}
		detail := at.Detail
		if detail == "" {
			detail = "exclusive AT control is unavailable"
		}
		fact.AT.Detail = detail
		return fact, errors.New(detail)
	}
	fact.Capabilities = agentmodem.Capabilities{
		CellularData: prober.guard != nil, SMSReceive: at.SMS, SMSSend: at.SMS,
	}
	if prober.guard != nil {
		if err := prober.guard.VerifyProtected(ctx, current.usb.PhysicalID, current.snapshot.NetPorts); err != nil {
			fact.Network.Guard = agentmodem.DataGuardFact{State: agentmodem.DataGuardFailed, Detail: bounded(err.Error(), 1024)}
			fact.Condition = agentmodem.DeviceDegraded
			return fact, fmt.Errorf("persistent cellular data guard: %w", err)
		}
		fact.Network.Guard.State = agentmodem.DataGuardProtected
	}
	var status agentat.SIMPINStatus
	var err error
	if fresh {
		status, err = prober.at.SIMPINStatusFresh(ctx, fact.EquipmentID)
	} else {
		status, err = prober.at.SIMPINStatus(ctx, fact.EquipmentID)
	}
	if err != nil {
		if simAbsent(err) {
			fact.SIM.State = agentmodem.SIMAbsent
			if !fresh {
				current.lastFact, current.lastFactAt = cloneFact(fact), now
			}
			return fact, nil
		}
		fact.AT.State = agentmodem.ATControlDegraded
		fact.AT.Port = ""
		fact.AT.Detail = bounded(err.Error(), 1024)
		fact.AT.CallSignalling, fact.AT.SMS, fact.AT.SIMAPDU = false, false, false
		return fact, err
	}
	fact.SIM.State = simState(string(status.State))
	fact.SIM.PINState = string(status.State)
	fact.SIM.ICCID = status.CardID
	fact.SIM.PINAttempts = cloneUint32(status.AttemptsRemaining)
	// Charged-call/SMS admission needs a fresh attachment, equipment identity,
	// AT owner and ICCID, but it must not wait behind optional presentation
	// queries. The periodic topology probe enriches those fields separately.
	if fresh {
		return fact, nil
	}
	metadataContext, cancelMetadata := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancelMetadata()
	exchangeMetadata := func(command string) ([]byte, error) {
		return prober.at.Exchange(metadataContext, fact.EquipmentID, command, 600*time.Millisecond)
	}
	if fact.SIM.State == agentmodem.SIMReady {
		if value, queryErr := exchangeMetadata("AT+CIMI"); queryErr == nil {
			fact.SIM.IMSI = parseIMSI(value)
		}
		if value, queryErr := exchangeMetadata("AT+CNUM"); queryErr == nil {
			fact.SIM.MSISDNs = parseMSISDNs(value)
		}
		if value, queryErr := exchangeMetadata("AT+CSCA?"); queryErr == nil {
			fact.SIM.SMSC = parseSMSC(value)
			fact.SIM.Configured = fact.SIM.SMSC != ""
		}
	}
	if value, queryErr := exchangeMetadata("AT+CREG?"); queryErr == nil {
		fact.Network.Registration = parseRegistration(value)
	}
	if fact.Network.Registration == agentmodem.RegistrationUnknown {
		if value, queryErr := exchangeMetadata("AT+CEREG?"); queryErr == nil {
			fact.Network.Registration = parseRegistration(value)
		}
	}
	if value, queryErr := exchangeMetadata("AT+COPS?"); queryErr == nil {
		fact.Network.OperatorID, fact.Network.OperatorName = parseOperator(value)
	}
	if value, queryErr := exchangeMetadata("AT+CSQ"); queryErr == nil {
		fact.Network.SignalPercent = parseSignal(value)
	}
	if value, queryErr := exchangeMetadata("AT+CFUN?"); queryErr == nil {
		fact.Network.SoftwareRadio = parseRadio(value)
	}
	current.lastFact, current.lastFactAt = cloneFact(fact), now
	return fact, nil
}

func (prober *Prober) Operate(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return agentmodem.OperationResult{}, err
	}
	if err := agentmodem.ValidateOperationTarget(facts, operation); err != nil {
		return agentmodem.OperationResult{}, err
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
	facts, err := prober.probeLocked(ctx, true)
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
	return agentmodem.SIMAKAResult{Body: result.Body, SW1: result.SW1, SW2: result.SW2}, err
}

func (prober *Prober) EnterSIMPIN(ctx context.Context, request agentmodem.SIMPINRequest) (agentmodem.SIMPINResult, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return agentmodem.SIMPINResult{}, err
	}
	var target *agentmodem.Fact
	for index := range facts {
		if facts[index].AttachmentID == request.AttachmentID && facts[index].EquipmentID == request.EquipmentID &&
			facts[index].SIM.ICCID == request.CardID {
			if target != nil {
				return agentmodem.SIMPINResult{}, agentmodem.ErrOperationTargetReplaced
			}
			target = &facts[index]
		}
	}
	if target == nil {
		return agentmodem.SIMPINResult{}, agentmodem.ErrOperationTargetReplaced
	}
	if target.SIM.State != agentmodem.SIMLocked || target.SIM.PINState != string(agentat.SIMPINRequired) ||
		target.AT.State != agentmodem.ATControlReady {
		return agentmodem.SIMPINResult{}, agentmodem.ErrOperationUnavailable
	}
	result, err := prober.at.EnterSIMPIN(ctx, request.EquipmentID, request.CardID, request.PIN)
	return agentmodem.SIMPINResult{
		Attempted: result.Attempted, Ready: result.Status.State == agentat.SIMPINNotRequired,
		AttemptsRemaining: cloneUint32(result.Status.AttemptsRemaining),
	}, err
}

func (prober *Prober) Close() error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	var failures []error
	for _, claim := range prober.data {
		failures = append(failures, prober.stopDataLocked(claim.target))
	}
	if prober.at != nil {
		failures = append(failures, prober.at.Close())
		prober.at = nil
	}
	for uid := range prober.devices {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		failures = append(failures, prober.manager.Inhibit(ctx, uid, false))
		cancel()
		delete(prober.devices, uid)
	}
	if prober.rawRecovery != nil {
		failures = append(failures, prober.rawRecovery.Close())
		prober.rawRecovery = nil
	}
	if prober.manager != nil {
		failures = append(failures, prober.manager.Close())
		prober.manager = nil
	}
	return errors.Join(failures...)
}

func (prober *Prober) recoverRawHandoff(ctx context.Context, facts []agentmodem.Fact, record agentrawusb.RecoveryRecord) {
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
	if len(matches) != 1 || matches[0].AT.State != agentmodem.ATControlReady ||
		matches[0].Network.Guard.State != agentmodem.DataGuardProtected {
		prober.failRawRecovery(record.EquipmentID, now, "exact modem is absent or its AT/data guard is not ready")
		return
	}
	status, err := prober.at.SIMPINStatusFresh(ctx, record.EquipmentID)
	if err != nil || status.CardID != record.CardID {
		prober.failRawRecovery(record.EquipmentID, now, "fresh SIM identity is unavailable or differs")
		return
	}
	call, err := prober.at.VerifiedHangup(ctx, record.EquipmentID)
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

func unavailableFact(snapshot modemSnapshot, generation usbGeneration, detail string, connected bool) agentmodem.Fact {
	attachmentID := generation.AttachmentID
	if attachmentID == "" {
		digest := sha256.Sum256([]byte(snapshot.UID + "\x00" + snapshot.EquipmentID))
		attachmentID = "linux-mm-" + hex.EncodeToString(digest[:12])
	}
	data := agentmodem.DataUnknown
	if connected {
		data = agentmodem.DataConnected
	}
	detail = bounded(detail, 1024)
	return agentmodem.Fact{
		AttachmentID: attachmentID, EquipmentID: snapshot.EquipmentID,
		Manufacturer: snapshot.Manufacturer, Model: snapshot.Model, Firmware: snapshot.Firmware,
		Condition: agentmodem.DeviceDegraded, Detail: detail,
		AT:  agentmodem.ATControlFact{State: agentmodem.ATControlUnavailable, Detail: detail},
		SIM: agentmodem.SIMFact{State: snapshot.SIMState, ICCID: snapshot.ICCID, IMSI: snapshot.IMSI, MSISDNs: append([]string(nil), snapshot.MSISDNs...)},
		Network: agentmodem.NetworkFact{
			Registration: snapshot.Registration, OperatorID: snapshot.OperatorID, OperatorName: snapshot.OperatorName,
			SignalPercent: cloneUint32(snapshot.SignalPercent), SoftwareRadio: agentmodem.RadioUnknown,
			HardwareRadio: agentmodem.RadioUnknown, Data: data,
			Guard: agentmodem.DataGuardFact{State: agentmodem.DataGuardFailed, Detail: detail},
		},
	}
}

func simState(pin string) agentmodem.SIMState {
	switch pin {
	case "not_required":
		return agentmodem.SIMReady
	case "pin_required", "puk_required", "other_lock":
		return agentmodem.SIMLocked
	default:
		return agentmodem.SIMUnknown
	}
}

func simAbsent(err error) bool {
	detail := strings.ToLower(err.Error())
	return strings.Contains(detail, "sim not inserted") || strings.Contains(detail, "sim absent") ||
		strings.Contains(detail, "no sim") || strings.Contains(detail, "+cme error: 10")
}

func cloneUint32(value *uint32) *uint32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFact(source agentmodem.Fact) agentmodem.Fact {
	result := source
	result.SIM.MSISDNs = append([]string(nil), source.SIM.MSISDNs...)
	result.SIM.PINAttempts = cloneUint32(source.SIM.PINAttempts)
	result.Network.SignalPercent = cloneUint32(source.Network.SignalPercent)
	return result
}

func hasKey(values map[string]struct{}, key string) bool {
	_, exists := values[key]
	return exists
}

var _ io.Closer = (*Prober)(nil)
