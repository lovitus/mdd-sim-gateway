//go:build darwin

package darwinmodem

import (
	"context"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
)

func (*Prober) PolicyProfileMode() string { return "system_managed" }

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
	current := prober.find(target.AttachmentID, target.EquipmentID)
	if current == nil {
		return agentmodem.ErrOperationTargetReplaced
	}
	command := "AT+CFUN=4"
	if enabled {
		command = "AT+CFUN=1"
	}
	if _, err := current.owner.Exchange(ctx, command, 10*time.Second); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, err := current.owner.Exchange(ctx, "AT+CFUN?", 3*time.Second)
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
	return []agentpolicy.ProfileView{}, nil
}

func (prober *Prober) SavePolicyProfile(context.Context, agentpolicy.Target, agentpolicy.Profile) error {
	return errors.New("mobile-broadband profiles are system-managed on macOS")
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
