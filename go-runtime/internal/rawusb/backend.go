package rawusb

import (
	"errors"
	"strings"
)

const windowsUSBIPDBackend = "windows-usbipd-v1"

var ErrCaptureNotPresent = errors.New("persistent raw USB capture is not present")

type CapturedError struct {
	Device Device
	Err    error
}

func (err *CapturedError) Error() string { return err.Err.Error() }
func (err *CapturedError) Unwrap() error { return err.Err }

func DeviceFromCaptureError(err error) (Device, bool) {
	var captured *CapturedError
	if !errors.As(err, &captured) || captured == nil {
		return Device{}, false
	}
	return captured.Device, true
}

func SamePersistentDevice(left, right Device) bool {
	if left.Backend == windowsUSBIPDBackend && right.Backend == windowsUSBIPDBackend {
		return left.InstanceID != "" && strings.EqualFold(left.InstanceID, right.InstanceID) &&
			left.PersistentID != "" && strings.EqualFold(left.PersistentID, right.PersistentID) &&
			left.VendorID == right.VendorID && left.ProductID == right.ProductID && left.Serial == right.Serial
	}
	return left == right
}
