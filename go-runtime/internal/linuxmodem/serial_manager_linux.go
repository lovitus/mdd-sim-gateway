//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulario"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
)

type serialManager struct {
	inventory     serialInventory
	ensureStopped func(context.Context) error
}

func (manager *serialManager) Inventory(ctx context.Context) ([]modemSnapshot, error) {
	if manager.ensureStopped == nil {
		return nil, errors.New("serial mode requires confirmed ModemManager stop")
	}
	if err := manager.ensureStopped(ctx); err != nil {
		return nil, err
	}
	return manager.inventory.Inventory(ctx)
}
func (manager *serialManager) Inhibit(ctx context.Context, _ string, _ bool) error {
	if manager.ensureStopped == nil {
		return errors.New("serial ownership unavailable")
	}
	return manager.ensureStopped(ctx)
}
func (*serialManager) Connect(context.Context, dbus.ObjectPath, agentdata.Profile) (dataBearer, error) {
	return dataBearer{}, errors.New("cellular data unavailable in serial-only mode")
}
func (*serialManager) Disconnect(context.Context, dbus.ObjectPath) error {
	return errors.New("serial mode does not own a ModemManager bearer")
}
func (*serialManager) Close() error { return nil }

// NewManagedSerialProber reuses the existing AT/SIM owner with the original
// profiled discovery source. Service switching belongs to the explicit caller.
func NewManagedSerialProber(simAPDU bool, profilesJSON []byte, guard *linuxdataguard.Guard, agentID string, recovery *agentrawusb.RecoveryStore, recoveryOnly bool, ensureStopped func(context.Context) error) (*Prober, error) {
	if guard == nil || recovery == nil || strings.TrimSpace(agentID) == "" || ensureStopped == nil {
		return nil, errors.New("serial modem runtime requires persistent guard, recovery and service ownership")
	}
	profiles, err := parseSerialProfiles(profilesJSON)
	if err != nil {
		return nil, err
	}
	helper, err := cellulario.ResolveSibling("mdd-call-audio-helper")
	if err != nil {
		return nil, err
	}
	manager := &serialManager{inventory: serialInventory{sysRoot: "/sys", profiles: profiles, open: openLinuxAT, verifyProtected: guard.VerifyProtected}, ensureStopped: ensureStopped}
	prober, err := newProber(manager, simAPDU, helper, "/sys", agentat.Opener(openLinuxAT))
	if err != nil {
		return nil, err
	}
	prober.serialOnly = true
	prober.guard = guard
	prober.rawRecovery = recovery
	prober.recoveryOnly = recoveryOnly
	prober.sourceAgentID = agentID
	return prober, nil
}
