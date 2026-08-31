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
	StableID     string
	BusID        string
	VendorID     uint16
	ProductID    uint16
	Serial       string
	Backend      string
	InstanceID   string
	PersistentID string
}

type Exporter struct {
	device        Device
	local         usbip.LocalDevice
	listener      *connListener
	service       *usbip.ServerService
	preserve      func() error
	close         func() error
	transportOnce sync.Once
	transportErr  error
	once          sync.Once
	closeErr      error
}

func NewExporter(ctx context.Context, physicalID string) (*Exporter, error) {
	return newPlatformExporterForPhysicalID(ctx, physicalID)
}

// NewExporterFromDevice re-adopts a durable capture by the exact BusID and
// USB identity previously returned by sing-usbip. This path never falls back
// to a VID/PID-only match and is used only for an authenticated recovery fact.
func NewExporterFromDevice(ctx context.Context, expected Device) (*Exporter, error) {
	if strings.TrimSpace(expected.BusID) == "" || expected.VendorID == 0 || expected.ProductID == 0 {
		return nil, errors.New("raw USB recovery device identity is incomplete")
	}
	return newPlatformExporterFromDevice(ctx, expected)
}

func NewExporterFromPendingCapture(ctx context.Context, physicalID string) (*Exporter, error) {
	return newPlatformExporterFromPendingCapture(ctx, physicalID)
}

func ReleaseCapturedDevice(ctx context.Context, device Device) error {
	return releasePlatformCapturedDevice(ctx, device)
}

func newPlatformExporter(ctx context.Context, expected Device) (*Exporter, error) {
	if ctx == nil || strings.TrimSpace(expected.BusID) == "" {
		return nil, errors.New("raw USB platform capture identity is incomplete")
	}
	listener := newConnListener()
	service, err := usbip.NewServerService(ctx, usbip.ServerOptions{
		Logger:  dependencyLogger{component: "sing-usbip exporter"},
		Devices: []usbip.DeviceMatch{{BusID: expected.BusID}},
		Listen: func(context.Context) (net.Listener, error) {
			return listener, nil
		},
	})
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	if err := service.Start(); err != nil {
		_ = service.Close()
		_ = listener.Close()
		return nil, err
	}
	snapshot := service.DeviceSnapshot()
	if len(snapshot) != 1 {
		_ = service.Close()
		_ = listener.Close()
		return nil, errors.New("raw USB platform capture did not select exactly one device")
	}
	actual := Device{
		StableID: snapshot[0].StableID, BusID: snapshot[0].BusID,
		VendorID: snapshot[0].VendorID, ProductID: snapshot[0].ProductID, Serial: snapshot[0].Serial,
	}
	if actual.BusID != expected.BusID || expected.VendorID != 0 && actual.VendorID != expected.VendorID ||
		expected.ProductID != 0 && actual.ProductID != expected.ProductID ||
		expected.Serial != "" && actual.Serial != expected.Serial {
		_ = service.Close()
		_ = listener.Close()
		return nil, errors.New("raw USB platform capture identity changed")
	}
	return &Exporter{device: actual, listener: listener, service: service}, nil
}

func newExporter(ctx context.Context, local usbip.LocalDevice, device Device) (*Exporter, error) {
	host := usbip.NewDynamicHost(nil)
	if _, err := host.AddDevice(usbip.ProvidedDeviceInfo{
		Entry: local.Entry(), StableID: local.StableID(),
	}, local); err != nil {
		_ = local.Close()
		return nil, err
	}
	listener := newConnListener()
	service, err := usbip.NewDynamicServerService(ctx, usbip.ServerOptions{
		Logger:  dependencyLogger{component: "sing-usbip exporter"},
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
	if exporter == nil {
		return errors.New("raw USB exporter is unavailable")
	}
	if exporter.listener == nil {
		return errors.New("raw USB exporter is unavailable")
	}
	return exporter.listener.Enqueue(ctx, conn)
}

func (exporter *Exporter) Close() error {
	if exporter == nil {
		return nil
	}
	exporter.once.Do(func() {
		if exporter.close != nil {
			exporter.closeErr = exporter.close()
			return
		}
		exporter.closeErr = exporter.closeTransport()
	})
	return exporter.closeErr
}

func (exporter *Exporter) closeTransport() error {
	if exporter == nil {
		return nil
	}
	exporter.transportOnce.Do(func() {
		var localErr, serviceErr, listenerErr error
		if exporter.local != nil {
			localErr = exporter.local.Close()
		}
		if exporter.service != nil {
			serviceErr = exporter.service.Close()
		}
		if exporter.listener != nil {
			listenerErr = exporter.listener.Close()
		}
		exporter.transportErr = errors.Join(serviceErr, listenerErr, localErr)
	})
	return exporter.transportErr
}

// Preserve stops only process-owned transport resources. The platform's
// durable capture remains in place and can be adopted by the next Agent
// process. Only Close is allowed to perform an explicit local adapted release.
func (exporter *Exporter) Preserve() error {
	if exporter == nil || exporter.preserve == nil {
		return nil
	}
	return exporter.preserve()
}

func openDevice(physicalID string) (usbip.LocalDevice, Device, error) {
	physicalID = strings.TrimSpace(physicalID)
	if physicalID == "" {
		return nil, Device{}, errors.New("raw USB physical identity is empty")
	}
	return openDeviceID(localDeviceID(physicalID))
}

func openDeviceID(deviceID string) (usbip.LocalDevice, Device, error) {
	local, err := usbip.OpenLocalDevice(strings.TrimSpace(deviceID), true)
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
