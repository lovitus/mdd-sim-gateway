package windowsmbn

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func waitDataState(ctx context.Context, wanted agentmodem.DataState, probe func() (agentmodem.DataState, error)) error {
	if wanted == agentmodem.DataDisconnected {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := probe()
		if err != nil {
			return err
		}
		if state == wanted {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("MBN connection wait: wanted=%s observed=%s: %w", wanted, state, ctx.Err())
		case <-ticker.C:
		}
	}
}

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
