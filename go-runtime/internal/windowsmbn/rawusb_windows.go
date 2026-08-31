//go:build windows && (amd64 || arm64)

package windowsmbn

import (
	"context"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
)

// AcquireRawUSB hands one exact, idle and persistently quarantined modem from
// the adapted Windows owner to sing-usbip. The caller already holds the paid
// operation coordinator; this method adds a fresh OS/card/data/call proof and
// releases the child AT handle before the whole USB parent is captured.
func (prober *Prober) AcquireRawUSB(ctx context.Context, target agentrawusb.SourceTarget) (string, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if current, exists := prober.raw[target.EquipmentID]; exists {
		if sameRawTarget(current.target, target) {
			return current.physicalID, nil
		}
		return "", errors.New("another raw USB session owns this modem")
	}
	if prober.guard == nil {
		return "", errors.New("persistent cellular data guard is unavailable")
	}
	if prober.data[target.EquipmentID] != nil {
		return "", errors.New("cellular data borrowing is active")
	}
	if prober.rawRecovery == nil || target.SourceAgentID != prober.sourceAgentID {
		return "", errors.New("raw USB handoff recovery is unavailable")
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
	facts, err := prober.rawProbe(ctx)
	if err != nil {
		return "", err
	}
	var selected *agentmodem.Fact
	for index := range facts {
		fact := &facts[index]
		if fact.AttachmentID != target.AttachmentID || fact.EquipmentID != target.EquipmentID ||
			fact.SIM.ICCID != target.CardID {
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
	call, err := prober.rawCallStatus(ctx, target.EquipmentID)
	if err != nil {
		return "", err
	}
	if !call.Authoritative || call.State != "idle" {
		return "", errors.New("modem call state is not authoritatively idle")
	}
	status, err := prober.freshSIMPINStatus(ctx, target.EquipmentID)
	if err != nil {
		return "", err
	}
	if status.CardID != target.CardID || status.State != agentat.SIMPINNotRequired {
		return "", agentmodem.ErrOperationTargetReplaced
	}
	record := recoveryRecord(target, time.Now().UTC())
	if _, _, err := prober.rawRecovery.Arm(record); err != nil {
		return "", err
	}
	physicalID, err := prober.rawReleaseAT(target.EquipmentID)
	if err != nil {
		return "", err
	}
	prober.raw[target.EquipmentID] = rawClaim{target: target, physicalID: physicalID}
	return physicalID, nil
}

// ReleaseRawUSB removes only the matching source claim. Closing sing-usbip's
// exporter has already returned the physical parent to Windows; the next
// regular probe re-discovers and re-proves its adapted AT owner.
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
