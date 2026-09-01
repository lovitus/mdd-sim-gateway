//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
)

func (*Prober) PolicyProfileMode() string { return "agent" }

func (prober *Prober) SetPolicyRadio(ctx context.Context, target agentpolicy.Target, enabled bool) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return err
	}
	if !exactPolicyTarget(facts, target) {
		return agentmodem.ErrOperationTargetReplaced
	}
	command := "AT+CFUN=4"
	if enabled {
		command = "AT+CFUN=1"
	}
	if _, err := prober.at.Exchange(ctx, target.EquipmentID, command, 10*time.Second); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, err := prober.at.Exchange(ctx, target.EquipmentID, "AT+CFUN?", 3*time.Second)
		if err == nil && (parseRadio(response) == agentmodem.RadioOn) == enabled {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("modem software radio did not converge")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (prober *Prober) ListPolicyProfiles(context.Context, agentpolicy.Target) ([]agentpolicy.ProfileView, error) {
	// Linux adapted mode owns the APN/auth profile in the Agent's 0600 policy
	// store and supplies it to ModemManager Simple.Connect on demand.
	return []agentpolicy.ProfileView{}, nil
}

func (prober *Prober) SavePolicyProfile(ctx context.Context, target agentpolicy.Target, _ agentpolicy.Profile) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return err
	}
	if !exactPolicyTarget(facts, target) {
		return agentmodem.ErrOperationTargetReplaced
	}
	return nil
}

func exactPolicyTarget(facts []agentmodem.Fact, target agentpolicy.Target) bool {
	matches := 0
	for _, fact := range facts {
		if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID &&
			fact.SIM.ICCID == target.CardID && fact.SIM.State == agentmodem.SIMReady &&
			fact.SIM.SessionGeneration == target.SIMSessionGeneration {
			matches++
		}
	}
	return matches == 1
}
