//go:build windows && (amd64 || arm64)

package windowsmbn

import (
	"context"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
)

// AcquireRawUSB hands one exact, idle and persistently quarantined modem from
// the adapted Windows owner to sing-usbip. The caller already holds the paid
// operation coordinator; this method adds a fresh OS/card/data/call proof and
// releases the child AT handle before the whole USB parent is captured.
func (prober *Prober) AcquireRawUSB(ctx context.Context, target agentrawusb.SourceTarget) (agentrawusb.SourceClaim, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if current, exists := prober.raw[target.EquipmentID]; exists {
		if sameRawTarget(current.target, target) {
			return current.claim, nil
		}
		if target.Recovering && sameRawCapture(current.target, target) && !current.transportOwned {
			current.target = target
			current.transportOwned = true
			prober.raw[target.EquipmentID] = current
			return current.claim, nil
		}
		return agentrawusb.SourceClaim{}, errors.New("another raw USB session owns this modem")
	}
	if prober.guard == nil {
		return agentrawusb.SourceClaim{}, errors.New("persistent cellular data guard is unavailable")
	}
	if prober.data[target.EquipmentID] != nil {
		return agentrawusb.SourceClaim{}, errors.New("cellular data borrowing is active")
	}
	if prober.rawRecovery == nil || target.SourceAgentID != prober.sourceAgentID {
		return agentrawusb.SourceClaim{}, errors.New("raw USB handoff recovery is unavailable")
	}
	if target.Recovering {
		record, err := prober.rawRecovery.RecoveryForTarget(target)
		if err != nil {
			return agentrawusb.SourceClaim{}, err
		}
		if record.USB == nil || record.ReleaseRequested {
			return agentrawusb.SourceClaim{}, errors.New("raw USB recovery capture is not resumable")
		}
		device := rawusb.Device{
			StableID: record.USB.StableID, BusID: record.USB.BusID,
			VendorID: record.USB.VendorID, ProductID: record.USB.ProductID, Serial: record.USB.Serial,
		}
		claim := agentrawusb.SourceClaim{Device: &device}
		prober.raw[target.EquipmentID] = rawClaim{target: target, claim: claim, transportOwned: true}
		return claim, nil
	}
	if records, err := prober.rawRecovery.Records(); err != nil {
		return agentrawusb.SourceClaim{}, err
	} else {
		for _, record := range records {
			if record.EquipmentID == target.EquipmentID {
				return agentrawusb.SourceClaim{}, errors.New("raw USB recovery debt blocks a new handoff")
			}
		}
	}
	facts, err := prober.rawProbe(ctx)
	if err != nil {
		return agentrawusb.SourceClaim{}, err
	}
	var selected *agentmodem.Fact
	for index := range facts {
		fact := &facts[index]
		if fact.AttachmentID != target.AttachmentID || fact.EquipmentID != target.EquipmentID ||
			fact.SIM.ICCID != target.CardID {
			continue
		}
		if selected != nil {
			return agentrawusb.SourceClaim{}, agentmodem.ErrOperationTargetReplaced
		}
		selected = fact
	}
	if selected == nil || selected.Condition != agentmodem.DeviceReady || selected.SIM.State != agentmodem.SIMReady ||
		selected.AT.State != agentmodem.ATControlReady {
		return agentrawusb.SourceClaim{}, agentmodem.ErrOperationTargetReplaced
	}
	if selected.Network.Guard.State != agentmodem.DataGuardProtected || selected.Network.Data != agentmodem.DataDisconnected {
		return agentrawusb.SourceClaim{}, errors.New("modem data path is not idle and persistently quarantined")
	}
	call, err := prober.rawCallStatus(ctx, target.EquipmentID)
	if err != nil {
		return agentrawusb.SourceClaim{}, err
	}
	if !call.Authoritative || call.State != "idle" {
		return agentrawusb.SourceClaim{}, errors.New("modem call state is not authoritatively idle")
	}
	status, err := prober.freshSIMPINStatus(ctx, target.EquipmentID)
	if err != nil {
		return agentrawusb.SourceClaim{}, err
	}
	if status.CardID != target.CardID || status.State != agentat.SIMPINNotRequired {
		return agentrawusb.SourceClaim{}, agentmodem.ErrOperationTargetReplaced
	}
	record := recoveryRecord(target, time.Now().UTC())
	if _, _, err := prober.rawRecovery.Arm(record); err != nil {
		return agentrawusb.SourceClaim{}, err
	}
	physicalID, err := prober.rawReleaseAT(target.EquipmentID)
	if err != nil {
		return agentrawusb.SourceClaim{}, err
	}
	claim := agentrawusb.SourceClaim{PhysicalID: physicalID}
	prober.raw[target.EquipmentID] = rawClaim{target: target, claim: claim, transportOwned: true}
	return claim, nil
}

// ReleaseRawUSB removes only the matching source claim. Closing sing-usbip's
// exporter has already returned the physical parent to Windows; the next
// regular probe re-discovers and re-proves its adapted AT owner.
func (prober *Prober) ReleaseRawUSB(target agentrawusb.SourceTarget, releaseCapture bool) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current, exists := prober.raw[target.EquipmentID]
	if !exists {
		if releaseCapture && prober.rawRecovery != nil {
			_, err := prober.rawRecovery.RequestRelease(target)
			return err
		}
		return nil
	}
	if !sameRawTarget(current.target, target) {
		return agentmodem.ErrOperationTargetReplaced
	}
	if !releaseCapture {
		return nil
	}
	var closeErr error
	if current.exporter != nil {
		closeErr = current.exporter.Close()
	}
	delete(prober.raw, target.EquipmentID)
	if releaseCapture && prober.rawRecovery != nil {
		_, err := prober.rawRecovery.RequestRelease(target)
		return errors.Join(closeErr, err)
	}
	return closeErr
}

func (prober *Prober) RecordRawUSBDevice(target agentrawusb.SourceTarget, device rawusb.Device) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current, exists := prober.raw[target.EquipmentID]
	if !exists || !sameRawTarget(current.target, target) || prober.rawRecovery == nil {
		return agentmodem.ErrOperationTargetReplaced
	}
	_, err := prober.rawRecovery.BindUSBIdentity(target, agentrawusb.RecoveryUSBIdentity{
		StableID: device.StableID, BusID: device.BusID, VendorID: device.VendorID,
		ProductID: device.ProductID, Serial: device.Serial,
	})
	return err
}

func (prober *Prober) PreserveRawUSB(target agentrawusb.SourceTarget, exporter agentrawusb.Exporter) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current, exists := prober.raw[target.EquipmentID]
	if !exists || !sameRawTarget(current.target, target) || exporter == nil || current.exporter != nil {
		return agentmodem.ErrOperationTargetReplaced
	}
	current.exporter = exporter
	current.transportOwned = false
	prober.raw[target.EquipmentID] = current
	return nil
}

func (prober *Prober) TakeRawUSB(target agentrawusb.SourceTarget) (agentrawusb.Exporter, bool, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current, exists := prober.raw[target.EquipmentID]
	if !exists || current.exporter == nil {
		return nil, false, nil
	}
	if !sameRawTarget(current.target, target) {
		return nil, false, agentmodem.ErrOperationTargetReplaced
	}
	exporter := current.exporter
	current.exporter = nil
	current.transportOwned = true
	prober.raw[target.EquipmentID] = current
	return exporter, true, nil
}

func (prober *Prober) RecoveryRawUSB() []agentlink.RawUSBRecoveryFact {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if prober.rawRecovery == nil {
		return nil
	}
	records, err := prober.rawRecovery.Records()
	if err != nil {
		return nil
	}
	result := make([]agentlink.RawUSBRecoveryFact, 0, len(records))
	for _, record := range records {
		current, active := prober.raw[record.EquipmentID]
		if record.ReleaseRequested || record.USB == nil || active && current.transportOwned {
			continue
		}
		result = append(result, agentlink.RawUSBRecoveryFact{
			AttachmentID: record.AttachmentID, SessionGeneration: record.SessionGeneration,
			EquipmentID: record.EquipmentID, CardID: record.CardID, USBSessionID: record.USBSessionID,
			Device: agentlink.RawUSBDevice{
				BusID: record.USB.BusID, VendorID: record.USB.VendorID,
				ProductID: record.USB.ProductID, Serial: record.USB.Serial,
			},
			State: "capture_reserved",
		})
	}
	return result
}

func recoveryRecord(target agentrawusb.SourceTarget, armedAt time.Time) agentrawusb.RecoveryRecord {
	return agentrawusb.RecoveryRecord{
		SourceAgentID: target.SourceAgentID, SourceProcessGeneration: target.SourceProcessGeneration,
		AttachmentID: target.AttachmentID, SessionGeneration: target.SessionGeneration,
		EquipmentID: target.EquipmentID, CardID: target.CardID,
		USBSessionID: target.USBSessionID, ArmedAt: armedAt,
	}
}

func sameRawTarget(left, right agentrawusb.SourceTarget) bool {
	return left.SourceAgentID == right.SourceAgentID &&
		left.SourceProcessGeneration == right.SourceProcessGeneration && left.AttachmentID == right.AttachmentID &&
		left.SessionGeneration == right.SessionGeneration && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID && left.USBSessionID == right.USBSessionID && left.Recovering == right.Recovering
}

func sameRawCapture(left, right agentrawusb.SourceTarget) bool {
	return left.SourceAgentID == right.SourceAgentID && left.AttachmentID == right.AttachmentID &&
		left.SessionGeneration == right.SessionGeneration && left.EquipmentID == right.EquipmentID &&
		left.CardID == right.CardID
}
