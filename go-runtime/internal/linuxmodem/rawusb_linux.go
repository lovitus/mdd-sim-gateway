//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
)

// AcquireRawUSB hands one exact, idle and persistently quarantined Linux
// modem from the adapted AT owner to sing-usbip. The ModemManager inhibition
// remains live while the USB parent is exported so no second local owner can
// race the handoff.
func (prober *Prober) AcquireRawUSB(ctx context.Context, target agentrawusb.SourceTarget) (string, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if current, exists := prober.raw[target.EquipmentID]; exists {
		if sameRawTarget(current.target, target) {
			return current.physicalID, nil
		}
		return "", errors.New("another raw USB session owns this modem")
	}
	if prober.guard == nil || prober.rawRecovery == nil || target.SourceAgentID != prober.sourceAgentID {
		return "", errors.New("persistent Linux raw USB ownership is unavailable")
	}
	if records, err := prober.rawRecovery.Records(); err != nil {
		return "", err
	} else {
		for _, record := range records {
			if record.EquipmentID == target.EquipmentID {
				return "", errors.New("raw USB recovery debt blocks a new handoff")
			}
		}
	}
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return "", err
	}
	var selected *agentmodem.Fact
	for index := range facts {
		fact := &facts[index]
		if fact.AttachmentID != target.AttachmentID || fact.EquipmentID != target.EquipmentID || fact.SIM.ICCID != target.CardID {
			continue
		}
		if selected != nil {
			return "", agentmodem.ErrOperationTargetReplaced
		}
		selected = fact
	}
	if selected == nil || selected.Condition != agentmodem.DeviceReady || selected.SIM.State != agentmodem.SIMReady ||
		selected.AT.State != agentmodem.ATControlReady {
		return "", agentmodem.ErrOperationTargetReplaced
	}
	if selected.Network.Guard.State != agentmodem.DataGuardProtected || selected.Network.Data != agentmodem.DataDisconnected {
		return "", errors.New("modem data path is not idle and persistently quarantined")
	}
	call, err := prober.at.CallStatus(ctx, target.EquipmentID)
	if err != nil {
		return "", err
	}
	if !call.Authoritative || call.State != "idle" {
		return "", errors.New("modem call state is not authoritatively idle")
	}
	status, err := prober.at.SIMPINStatusFresh(ctx, target.EquipmentID)
	if err != nil {
		return "", err
	}
	if status.CardID != target.CardID || status.State != agentat.SIMPINNotRequired {
		return "", agentmodem.ErrOperationTargetReplaced
	}
	if _, _, err := prober.rawRecovery.Arm(recoveryRecord(target, time.Now().UTC())); err != nil {
		return "", err
	}
	physicalID, err := prober.at.ReleaseForRawUSB(target.EquipmentID)
	if err != nil {
		return "", err
	}
	prober.raw[target.EquipmentID] = rawClaim{target: target, physicalID: physicalID}
	return physicalID, nil
}

func (prober *Prober) ReleaseRawUSB(target agentrawusb.SourceTarget) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current, exists := prober.raw[target.EquipmentID]
	if !exists {
		return nil
	}
	if !sameRawTarget(current.target, target) {
		return agentmodem.ErrOperationTargetReplaced
	}
	delete(prober.raw, target.EquipmentID)
	return nil
}

func (prober *Prober) StartImport(ctx context.Context, device rawusb.Device, start, detach func() error) (string, error) {
	if prober.guard == nil {
		return "", errors.New("persistent Linux raw USB import guard is unavailable")
	}
	return prober.guard.StartImport(ctx, linuxdataguard.DeviceIdentity{
		VendorID: device.VendorID, ProductID: device.ProductID, Serial: device.Serial,
	}, start, detach)
}

func (prober *Prober) StopImport(ctx context.Context, physicalID string, detach func() error) error {
	if prober.guard == nil {
		return errors.New("persistent Linux raw USB import guard is unavailable")
	}
	return prober.guard.StopImport(ctx, physicalID, detach)
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
		left.CardID == right.CardID && left.USBSessionID == right.USBSessionID
}

var _ agentrawusb.SourceBackend = (*Prober)(nil)
var _ agentrawusb.ImportGuard = (*Prober)(nil)
