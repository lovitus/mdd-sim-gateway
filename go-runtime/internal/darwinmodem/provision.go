//go:build darwin

package darwinmodem

import (
	"context"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agenthost"
)

type provisionAT struct {
	prober *Prober
}

func (at provisionAT) SIMPINStatusFresh(ctx context.Context, equipmentID string) (agentat.SIMPINStatus, error) {
	at.prober.mu.Lock()
	defer at.prober.mu.Unlock()
	for _, current := range at.prober.devices {
		if current.owner != nil && current.owner.EquipmentID() == equipmentID {
			return current.owner.SIMPINStatusFull(ctx)
		}
	}
	return agentat.SIMPINStatus{}, errors.New("AT control owner is unavailable")
}

func (at provisionAT) Exchange(ctx context.Context, equipmentID, command string, timeout time.Duration) ([]byte, error) {
	at.prober.mu.Lock()
	defer at.prober.mu.Unlock()
	for _, current := range at.prober.devices {
		if current.owner != nil && current.owner.EquipmentID() == equipmentID {
			return current.owner.Exchange(ctx, command, timeout)
		}
	}
	return nil, errors.New("AT control owner is unavailable")
}

func (prober *Prober) ProvisionHardware() agenthost.ProvisionHardware {
	return agentat.NewProvisionHardware(provisionAT{prober: prober})
}
