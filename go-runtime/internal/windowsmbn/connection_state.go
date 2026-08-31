package windowsmbn

import (
	"errors"
	"syscall"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

// HRESULT_FROM_WIN32(ERROR_NOT_FOUND) is the documented IMbnInterface::GetConnection
// result when no connection is available or the device is not registered. In either
// case there is no active data bearer to leak or to block a raw-Modem handoff.
const windowsErrorNotFound = syscall.Errno(1168)

func dataStateFromConnectionError(err error) (agentmodem.DataState, bool) {
	if errors.Is(err, windowsErrorNotFound) {
		return agentmodem.DataDisconnected, true
	}
	return agentmodem.DataUnknown, false
}
