//go:build linux

package rawusb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func newPlatformExporterForPhysicalID(ctx context.Context, physicalID string) (*Exporter, error) {
	return newPlatformExporter(ctx, Device{BusID: localDeviceID(physicalID)})
}

func newPlatformExporterFromDevice(ctx context.Context, expected Device) (*Exporter, error) {
	return newPlatformExporter(ctx, expected)
}

func newPlatformExporterFromPendingCapture(ctx context.Context, physicalID string) (*Exporter, error) {
	busID := localDeviceID(physicalID)
	driver, err := os.Readlink(filepath.Join("/sys/bus/usb/devices", busID, "driver"))
	if errors.Is(err, os.ErrNotExist) || err == nil && filepath.Base(driver) != "usbip-host" {
		return nil, ErrCaptureNotPresent
	}
	if err != nil {
		return nil, err
	}
	return newPlatformExporter(ctx, Device{BusID: busID})
}

func releasePlatformCapturedDevice(ctx context.Context, device Device) error {
	exporter, err := newPlatformExporterFromDevice(ctx, device)
	if err != nil {
		return err
	}
	return exporter.Close()
}
