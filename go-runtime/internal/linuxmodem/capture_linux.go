//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"fmt"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawcapture"
)

func (prober *Prober) ProveRawCapture(ctx context.Context, pair rawcapture.Pair) (rawcapture.Proof, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	delete(prober.localCapture, pair.EquipmentID)
	if prober.guard == nil || prober.data[pair.EquipmentID] != nil {
		return rawcapture.Proof{}, errors.New("persistent guard is unavailable or cellular data is active")
	}
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return rawcapture.Proof{}, err
	}
	fact, err := exactCaptureFact(facts, pair)
	if err != nil {
		return rawcapture.Proof{}, err
	}
	call, err := prober.at.CallStatus(ctx, pair.EquipmentID)
	if err != nil || !call.Authoritative || call.State != "idle" {
		return rawcapture.Proof{}, errors.Join(fmt.Errorf("read fresh call state: %w", err),
			errors.New("modem call state is not authoritatively idle"))
	}
	status, err := prober.at.SIMPINStatusFresh(ctx, pair.EquipmentID)
	if err != nil || status.CardID != pair.CardID || status.State != agentat.SIMPINNotRequired {
		return rawcapture.Proof{}, errors.Join(fmt.Errorf("read fresh SIM identity: %w", err),
			agentmodem.ErrOperationTargetReplaced)
	}
	physicalID, err := prober.at.PhysicalID(pair.EquipmentID)
	if err != nil {
		return rawcapture.Proof{}, fmt.Errorf("resolve physical USB parent: %w", err)
	}
	prober.localCapture[pair.EquipmentID] = true
	return rawcapture.Proof{Pair: pair, AttachmentID: fact.AttachmentID,
		PhysicalID: physicalID}, nil
}

func (prober *Prober) ReleaseATForRawCapture(ctx context.Context, proof rawcapture.Proof) (string, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	delete(prober.localCapture, proof.EquipmentID)
	defer func() { prober.localCapture[proof.EquipmentID] = true }()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return "", err
	}
	fact, err := exactCaptureFact(facts, proof.Pair)
	if err != nil || fact.AttachmentID != proof.AttachmentID {
		return "", agentmodem.ErrOperationTargetReplaced
	}
	physicalID, err := prober.at.ReleaseForRawUSB(proof.EquipmentID)
	if err != nil || physicalID != proof.PhysicalID {
		return "", errors.Join(err, agentmodem.ErrOperationTargetReplaced)
	}
	prober.localCapture[proof.EquipmentID] = true
	return physicalID, nil
}

func (prober *Prober) VerifyAdapted(ctx context.Context, pair rawcapture.Pair) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	delete(prober.localCapture, pair.EquipmentID)
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		prober.localCapture[pair.EquipmentID] = true
		return err
	}
	if _, err := exactCaptureFact(facts, pair); err != nil {
		prober.localCapture[pair.EquipmentID] = true
		return err
	}
	call, err := prober.at.VerifiedHangup(ctx, pair.EquipmentID)
	if err != nil || call.State != "idle" || !call.Authoritative || !call.TerminalConfirmed {
		prober.localCapture[pair.EquipmentID] = true
		return errors.Join(err, errors.New("terminal physical hangup is not confirmed"))
	}
	return nil
}

func exactCaptureFact(facts []agentmodem.Fact, pair rawcapture.Pair) (agentmodem.Fact, error) {
	var result agentmodem.Fact
	matches := 0
	for _, fact := range facts {
		if fact.EquipmentID == pair.EquipmentID && fact.SIM.ICCID == pair.CardID {
			result, matches = fact, matches+1
		}
	}
	if matches != 1 || result.Condition != agentmodem.DeviceReady || result.SIM.State != agentmodem.SIMReady ||
		result.AT.State != agentmodem.ATControlReady ||
		result.Network.Data != agentmodem.DataDisconnected || result.Network.Guard.State != agentmodem.DataGuardProtected {
		return agentmodem.Fact{}, agentmodem.ErrOperationTargetReplaced
	}
	return result, nil
}

var _ rawcapture.Backend = (*Prober)(nil)
