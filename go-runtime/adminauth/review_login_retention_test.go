package adminauth

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestReviewOverloadedLoginsDoNotAllocatePeerHistory(t *testing.T) {
	manager := testManager(t, false, time.Now)
	// This is the same locked state reached by two admitted derivations.
	for i := 0; i < maxLoginDerivations; i++ {
		manager.loginInFlight[fmt.Sprintf("busy-%d", i)] = true
	}
	manager.derivePassword = func(_, _ []byte) ([]byte, error) { t.Fatal("overload reached scrypt"); return nil, nil }
	for i := 0; i < 1000; i++ {
		_, err := manager.Login("fanli", "wrong", fmt.Sprintf("2001:db8::%x", i))
		var throttle *ThrottleError
		if !errors.As(err, &throttle) {
			t.Fatalf("overload error=%v", err)
		}
	}
	if len(manager.failures) != 0 {
		t.Fatalf("overloaded requests allocated %d peer-history entries", len(manager.failures))
	}
}

func TestReviewLoginHistoryHasBoundAndExpires(t *testing.T) {
	now := time.Now()
	manager := testManager(t, false, func() time.Time { return now })
	manager.derivePassword = func(_, _ []byte) ([]byte, error) { return nil, nil }
	// 4096 is the admission budget: retain existing-peer throttles rather than
	// evicting them to make room for unlimited new source addresses.
	for i := 0; i < 5000; i++ {
		_, _ = manager.Login("fanli", "wrong", fmt.Sprintf("peer-%d", i))
	}
	if len(manager.failures) > 4096 {
		t.Fatalf("unbounded peer history: %d", len(manager.failures))
	}
	now = now.Add(16 * time.Minute)
	_, err := manager.Login("fanli", "wrong", "fresh-peer")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expired history prevented a fresh attempt: %v", err)
	}
	if len(manager.failures) != 1 {
		t.Fatalf("expired peer histories retained: %d", len(manager.failures))
	}
}
