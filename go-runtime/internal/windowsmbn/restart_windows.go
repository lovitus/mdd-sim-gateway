//go:build windows && (amd64 || arm64)

package windowsmbn

import (
	"context"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowspnp"
)

func (prober *Prober) SoftRestart(ctx context.Context, target agentmodem.RecoveryTarget) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if prober.restartPending {
		return agentmodem.ErrRecoveryPending
	}
	_, rawOwned := prober.raw[target.EquipmentID]
	if prober.data[target.EquipmentID] != nil || rawOwned {
		return agentmodem.ErrOperationUnavailable
	}
	facts, err := prober.probeLocked(ctx)
	if err != nil {
		return err
	}
	matches := 0
	for _, fact := range facts {
		if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID &&
			fact.SIM.ICCID == target.CardID && fact.SIM.SessionGeneration == target.SIMSessionGeneration &&
			fact.SIM.State == agentmodem.SIMReady && fact.AT.State == agentmodem.ATControlReady {
			matches++
		}
	}
	if matches != 1 {
		return agentmodem.ErrOperationTargetReplaced
	}
	call, err := prober.at.CallStatus(ctx, target.EquipmentID)
	if err != nil || !call.Authoritative || call.State != "idle" || call.VoiceCalls != 0 || call.IncomingCalls != 0 {
		return agentmodem.ErrOperationUnavailable
	}
	pnpTarget, err := windowspnp.ResolveRestartTarget(ctx, target.AttachmentID)
	if err != nil {
		return agentmodem.ErrOperationUnavailable
	}
	prober.restartPending = true
	go func() {
		timer := time.NewTimer(1500 * time.Millisecond)
		<-timer.C
		restartContext, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		_ = windowspnp.RestartDevice(restartContext, pnpTarget)
		cancel()
		prober.mu.Lock()
		prober.restartPending = false
		prober.mu.Unlock()
	}()
	return nil
}

var _ agentmodem.Restarter = (*Prober)(nil)
