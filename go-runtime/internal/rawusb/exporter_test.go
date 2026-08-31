//go:build linux || windows

package rawusb

import (
	"runtime"
	"testing"
)

func TestLocalDeviceIDUsesPlatformStableIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		const input = `usb\vid_2c7c&pid_0125\0123456789abcdef`
		const want = `windows-instance:USB\VID_2C7C&PID_0125\0123456789ABCDEF`
		if got := localDeviceID(input); got != want {
			t.Fatalf("local device ID=%q want=%q", got, want)
		}
		return
	}
	if got := localDeviceID("/sys/devices/pci0000:00/usb1/1-2"); got != "1-2" {
		t.Fatalf("local device ID=%q", got)
	}
}
