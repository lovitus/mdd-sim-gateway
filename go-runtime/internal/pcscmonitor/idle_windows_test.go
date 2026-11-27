//go:build windows

package pcscmonitor

import (
	"errors"
	"testing"

	"github.com/ebfe/scard"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

func TestWindowsIdleSmartCardServiceEmptyInventory(t *testing.T) {
	present, err := querySmartCardReaderPresent()
	if err != nil {
		t.Fatalf("query PnP smart card readers: %v", err)
	}
	if present {
		t.Skip("host has a present smart card reader")
	}
	monitor, err := (Factory{}).Open(t.Context())
	if err != nil {
		t.Fatalf("WINDOWS_IDLE_PCSC: open failed on readerless Windows host: %v", err)
	}
	defer monitor.Close()
	readers, err := monitor.Scan(t.Context())
	if err != nil {
		t.Fatalf("WINDOWS_IDLE_PCSC: scan failed on readerless Windows host: %v", err)
	}
	if len(readers) != 0 {
		t.Fatalf("expected empty reader inventory, got %+v", readers)
	}
}

func TestWindowsIdleSmartCardServiceFailClosedGuards(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		state     svc.State
		startType uint32
		svcErr    error
		present   bool
		pnpErr    error
		want      bool
	}{
		{name: "idle-manual-no-service", err: scard.ErrNoService, state: svc.Stopped, startType: windows.SERVICE_DEMAND_START, want: true},
		{name: "idle-auto-service-stopped", err: scard.ErrServiceStopped, state: svc.StopPending, startType: windows.SERVICE_AUTO_START, want: true},
		{name: "other-pcsc-error", err: scard.ErrInternalError, state: svc.Stopped, startType: windows.SERVICE_DEMAND_START, want: false},
		{name: "disabled-service", err: scard.ErrNoService, state: svc.Stopped, startType: windows.SERVICE_DISABLED, want: false},
		{name: "running-service-still-failing", err: scard.ErrNoService, state: svc.Running, startType: windows.SERVICE_DEMAND_START, want: false},
		{name: "scm-query-error", err: scard.ErrNoService, state: svc.Stopped, startType: windows.SERVICE_DEMAND_START, svcErr: errors.New("scm unavailable"), want: false},
		{name: "reader-present-while-service-stopped", err: scard.ErrNoService, state: svc.Stopped, startType: windows.SERVICE_DEMAND_START, present: true, want: false},
		{name: "pnp-query-error", err: scard.ErrNoService, state: svc.Stopped, startType: windows.SERVICE_DEMAND_START, pnpErr: errors.New("setupapi error"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := allowIdleNoReadersWith(
				tc.err,
				func() (svc.State, uint32, error) { return tc.state, tc.startType, tc.svcErr },
				func() (bool, error) { return tc.present, tc.pnpErr },
			)
			if got != tc.want {
				t.Fatalf("allowIdleNoReadersWith() = %t, want %t", got, tc.want)
			}
		})
	}
}
