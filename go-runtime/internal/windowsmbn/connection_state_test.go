package windowsmbn

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func TestDocumentedMissingMBNConnectionIsDisconnected(t *testing.T) {
	for _, err := range []error{windowsErrorNotFound, fmt.Errorf("GetConnection: %w", windowsErrorNotFound)} {
		state, known := dataStateFromConnectionError(err)
		if !known || state != agentmodem.DataDisconnected {
			t.Fatalf("state=%q known=%t err=%v", state, known, err)
		}
	}
	for _, err := range []error{nil, syscall.Errno(5), errors.New("pending")} {
		state, known := dataStateFromConnectionError(err)
		if known || state != agentmodem.DataUnknown {
			t.Fatalf("unrelated error state=%q known=%t err=%v", state, known, err)
		}
	}
}
