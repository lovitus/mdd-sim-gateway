//go:build darwin

package main

/*
#cgo CFLAGS: -fblocks
#cgo LDFLAGS: -framework AVFoundation -framework Foundation

int mdd_request_microphone_authorization(unsigned int timeout_ms);
*/
import "C"

import "errors"

func requestMicrophoneAuthorization() error {
	switch int(C.mdd_request_microphone_authorization(30000)) {
	case 0:
		return nil
	case 1:
		return errors.New("macOS microphone permission was denied; allow MDD Agent in Privacy & Security > Microphone")
	case 2:
		return errors.New("macOS microphone permission is restricted by system policy")
	case 3:
		return errors.New("macOS microphone permission request timed out without a user decision")
	default:
		return errors.New("macOS microphone permission state is unavailable")
	}
}
