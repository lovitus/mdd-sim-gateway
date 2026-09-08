package recovery

import (
	"errors"
	"math"
	"time"
)

// ContinuousRetry is copied from ec620942 control/app/main.py:apply_health:
// the legacy retry count represents elapsed intervals, not request failures.
type ContinuousRetry struct {
	Max      int `json:"max" yaml:"max"`
	Interval int `json:"interval" yaml:"interval"`
}

func DefaultContinuousRetry() ContinuousRetry { return ContinuousRetry{Max: 3, Interval: 40} }

func (policy ContinuousRetry) Budget() (time.Duration, error) {
	if policy.Max < 1 || policy.Interval < 5 || int64(policy.Interval) > math.MaxInt64/int64(time.Second) ||
		int64(policy.Max) > math.MaxInt64/(int64(policy.Interval)*int64(time.Second)) {
		return 0, errors.New("invalid continuous retry window")
	}
	return time.Duration(policy.Max) * time.Duration(policy.Interval) * time.Second, nil
}

func (policy ContinuousRetry) Count(elapsed time.Duration) int {
	if _, err := policy.Budget(); err != nil || elapsed < 0 {
		return 0
	}
	count := elapsed / (time.Duration(policy.Interval) * time.Second)
	if count >= time.Duration(policy.Max) {
		return policy.Max
	}
	return int(count) + 1
}
