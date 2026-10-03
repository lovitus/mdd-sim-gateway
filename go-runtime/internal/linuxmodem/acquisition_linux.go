//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
)

// The platform guard remains the sole owner of kernel isolation and permits.
type cellularGuard interface {
	VerifyProtected(context.Context, string, []string) error
	OpenDataPermit(context.Context, string) (linuxdataguard.DataPermit, error)
	CloseDataPermit(context.Context, linuxdataguard.DataPermit) error
	ConfigureDataRoute(context.Context, linuxdataguard.DataPermit, string, netip.Prefix, netip.Addr) (linuxdataguard.DataRoute, error)
	CleanupDataRoute(*linuxdataguard.DataRoute) error
	StartImport(context.Context, linuxdataguard.DeviceIdentity, func() error, func() error) (string, error)
	StopImport(context.Context, string, func() error) error
}

func (prober *Prober) acquisitionIdle(ctx context.Context, snapshot modemSnapshot) error {
	// Serial inventory already requires ModemManager to be stopped. It has
	// no MM Voice object and must not inherit an unconditional MM query.
	if prober.serialOnly {
		return nil
	}
	observer, ok := prober.manager.(interface {
		VoiceIdle(context.Context, dbus.ObjectPath) (bool, error)
	})
	if !ok {
		return errors.New("ModemManager call inventory unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	idle, err := observer.VoiceIdle(ctx, snapshot.ObjectPath)
	if err != nil {
		return err
	}
	if !idle {
		return errors.New("ModemManager call is active or its state is unknown")
	}
	return nil
}
