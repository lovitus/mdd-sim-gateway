//go:build linux || windows

package rawusb

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	usbip "github.com/sagernet/sing-usbip"
)

// Device is the exact local USB identity selected from a previously verified
// Modem physical parent. Vendor/product-only matching is deliberately absent.
type Device struct {
	StableID  string
	BusID     string
	VendorID  uint16
	ProductID uint16
	Serial    string
}

type Exporter struct {
	device   Device
	local    usbip.LocalDevice
	listener *connListener
	service  *usbip.ServerService
	once     sync.Once
	closeErr error
}

func NewExporter(ctx context.Context, physicalID string) (*Exporter, error) {
	local, device, err := openDevice(physicalID)
	if err != nil {
		return nil, err
	}
	host := usbip.NewDynamicHost(nil)
	if _, err := host.AddDevice(usbip.ProvidedDeviceInfo{
		Entry: local.Entry(), StableID: local.StableID(),
	}, local); err != nil {
		_ = local.Close()
		return nil, err
	}
	listener := newConnListener()
	service, err := usbip.NewDynamicServerService(ctx, usbip.ServerOptions{
		Devices: []usbip.DeviceMatch{{BusID: device.BusID}},
		Listen: func(context.Context) (net.Listener, error) {
			return listener, nil
		},
	}, host)
	if err != nil {
		_ = listener.Close()
		_ = local.Close()
		return nil, err
	}
	if err := service.Start(); err != nil {
		_ = service.Close()
		_ = listener.Close()
		_ = local.Close()
		return nil, err
	}
	return &Exporter{device: device, local: local, listener: listener, service: service}, nil
}

func (exporter *Exporter) Device() Device { return exporter.device }

func (exporter *Exporter) AcceptStream(ctx context.Context, conn net.Conn) error {
	if exporter == nil || exporter.listener == nil {
		return errors.New("raw USB exporter is unavailable")
	}
	return exporter.listener.Enqueue(ctx, conn)
}

func (exporter *Exporter) Close() error {
	if exporter == nil {
		return nil
	}
	exporter.once.Do(func() {
		exporter.closeErr = errors.Join(exporter.service.Close(), exporter.listener.Close(), exporter.local.Close())
	})
	return exporter.closeErr
}

func openDevice(physicalID string) (usbip.LocalDevice, Device, error) {
	physicalID = strings.TrimSpace(physicalID)
	if physicalID == "" {
		return nil, Device{}, errors.New("raw USB physical identity is empty")
	}
	local, err := usbip.OpenLocalDevice(localDeviceID(physicalID), true)
	if err != nil {
		return nil, Device{}, err
	}
	entry := local.Entry()
	busID := entry.Info.BusIDString()
	if busID == "" {
		_ = local.Close()
		return nil, Device{}, errors.New("raw USB device has no bus identity")
	}
	return local, Device{
		StableID:  local.StableID(),
		BusID:     busID,
		VendorID:  entry.Info.IDVendor,
		ProductID: entry.Info.IDProduct,
		Serial:    entry.Serial,
	}, nil
}

func localDeviceID(physicalID string) string {
	if runtime.GOOS == "windows" {
		return "windows-instance:" + strings.ToUpper(strings.TrimSpace(physicalID))
	}
	return filepath.Base(filepath.Clean(strings.TrimSpace(physicalID)))
}
