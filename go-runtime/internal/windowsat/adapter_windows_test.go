//go:build windows && (amd64 || arm64)

package windowsat

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestPortBusyRecognizesWindowsExclusiveOpenFailures(t *testing.T) {
	for _, err := range []error{windows.ERROR_BUSY, windows.ERROR_SHARING_VIOLATION, windows.ERROR_ACCESS_DENIED} {
		if !portBusy(err) {
			t.Fatalf("portBusy(%v)=false", err)
		}
	}
}
