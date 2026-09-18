package usernet

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

type livenessReviewSession struct {
	*memorySession
	entered chan struct{}
	release chan struct{}
}

func (s *livenessReviewSession) RunLivenessMaintenance(ctx context.Context) error {
	close(s.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return errors.New("confirmed liveness failure")
	}
}

// Runs on the old source as well: it proves the optional maintenance seam
// actually gets driven and does not take over paid-call teardown authority.
func TestLivenessReviewStackDrivesMaintenanceWithoutPrematureClose(t *testing.T) {
	packets, _ := memorySessionPair()
	s := &livenessReviewSession{packets, make(chan struct{}), make(chan struct{})}
	stack, err := Open(t.Context(), s, Config{Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeStackWithin(t, "liveness", stack)
	select {
	case <-s.entered:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("stack never drove liveness maintenance")
	}
	close(s.release)
	select {
	case err := <-stack.Errors():
		if err == nil {
			t.Fatal("liveness failure hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("no liveness fault observation")
	}
	select {
	case <-packets.closed:
		t.Fatal("liveness worker prematurely closed the owned tunnel")
	default:
	}
	if stack.ctx.Err() != nil {
		t.Fatal("liveness worker bypassed idle-only cleanup")
	}
}
