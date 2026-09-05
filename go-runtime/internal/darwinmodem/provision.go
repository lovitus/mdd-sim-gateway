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

type lockedProvisionAT struct{ prober *Prober }

func (at provisionAT) WithProvisionTransaction(ctx context.Context, callback func(agentat.ProvisionAT) error) error {
	if callback == nil {
		return errors.New("provision transaction callback is required")
	}
	at.prober.mu.Lock()
	defer at.prober.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return callback(lockedProvisionAT{prober: at.prober})
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

func (at provisionAT) CallStatus(ctx context.Context, equipmentID string) (agentat.CallState, error) {
	at.prober.mu.Lock()
	defer at.prober.mu.Unlock()
	return (lockedProvisionAT{prober: at.prober}).CallStatus(ctx, equipmentID)
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

func (at lockedProvisionAT) SIMPINStatusFresh(ctx context.Context, equipmentID string) (agentat.SIMPINStatus, error) {
	for _, current := range at.prober.devices {
		if current.owner != nil && current.owner.EquipmentID() == equipmentID {
			return current.owner.SIMPINStatusFull(ctx)
		}
	}
	return agentat.SIMPINStatus{}, errors.New("AT control owner is unavailable")
}

func (at lockedProvisionAT) CallStatus(ctx context.Context, equipmentID string) (agentat.CallState, error) {
	for _, current := range at.prober.devices {
		if current.owner != nil && current.owner.EquipmentID() == equipmentID {
			return current.owner.CallStatus(ctx)
		}
	}
	return agentat.CallState{}, errors.New("AT control owner is unavailable")
}

func (at lockedProvisionAT) Exchange(ctx context.Context, equipmentID, command string, timeout time.Duration) ([]byte, error) {
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
