package recovery

import (
	"testing"
	"time"
)

func TestLegacyContinuousRetryBudgetAndCount(t *testing.T) {
	policy := DefaultContinuousRetry()
	if budget, err := policy.Budget(); err != nil || budget != 120*time.Second {
		t.Fatal(budget, err)
	}
	for _, item := range []struct {
		elapsed time.Duration
		count   int
	}{{0, 1}, {39 * time.Second, 1}, {40 * time.Second, 2}, {80 * time.Second, 3}, {120 * time.Second, 3}, {-1, 0}} {
		if got := policy.Count(item.elapsed); got != item.count {
			t.Fatal(item, got)
		}
	}
	for _, policy := range []ContinuousRetry{{Max: 0, Interval: 40}, {Max: 3, Interval: 4}, {Max: int(^uint(0) >> 1), Interval: 40}} {
		if _, err := policy.Budget(); err == nil {
			t.Fatal("invalid budget accepted", policy)
		}
	}
}
