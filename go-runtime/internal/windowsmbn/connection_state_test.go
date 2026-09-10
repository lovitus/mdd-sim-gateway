package windowsmbn

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func TestDisconnectWaitRequiresConfirmedStateAndHonorsDeadline(t *testing.T) {
	for _, state := range []agentmodem.DataState{agentmodem.DataConnected, agentmodem.DataDisconnecting, agentmodem.DataUnknown} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := waitDataState(ctx, agentmodem.DataDisconnected, func() (agentmodem.DataState, error) { return state, nil })
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("state=%s unexpectedly converged: %v", state, err)
		}
	}
	if err := waitDataState(context.Background(), agentmodem.DataDisconnected, func() (agentmodem.DataState, error) {
		return agentmodem.DataDisconnected, nil
	}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("MBN read failed")
	if err := waitDataState(context.Background(), agentmodem.DataDisconnected, func() (agentmodem.DataState, error) {
		return agentmodem.DataUnknown, want
	}); !errors.Is(err, want) {
		t.Fatal("original MBN error lost", err)
	}
}

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
