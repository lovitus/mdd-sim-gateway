//go:build windows

package rawusb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	usbip "github.com/sagernet/sing-usbip"
)

// RecoverCapturedDevice uses sing-usbip's own Windows platform host to adopt
// and release one exact VBoxUSB-captured device. The in-memory listener never
// opens a TCP port; the service exists only long enough to run the upstream
// filter removal and PnP re-enumeration path.
func RecoverCapturedDevice(parent context.Context, device Device) error {
	if parent == nil || device.BusID == "" || device.VendorID == 0 || device.ProductID == 0 {
		return errors.New("invalid captured USB recovery identity")
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	listener := newConnListener()
	service, err := usbip.NewServerService(ctx, usbip.ServerOptions{
		Logger: dependencyLogger{component: "sing-usbip recovery"},
		Devices: []usbip.DeviceMatch{{
			BusID: device.BusID, VendorID: device.VendorID,
			ProductID: device.ProductID, Serial: device.Serial,
		}},
		Listen: func(context.Context) (net.Listener, error) { return listener, nil },
	})
	if err != nil {
		return err
	}
	if err := service.Start(); err != nil {
		return err
	}
	snapshot := service.DeviceSnapshot()
	match := len(snapshot) == 1 && snapshot[0].BusID == device.BusID &&
		snapshot[0].VendorID == device.VendorID && snapshot[0].ProductID == device.ProductID &&
		(device.Serial == "" || snapshot[0].Serial == device.Serial)
	closeErr := service.Close()
	if !match {
		return errors.Join(fmt.Errorf("captured USB device %s was not uniquely adopted", device.BusID), closeErr)
	}
	return closeErr
}
