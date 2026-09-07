// SPDX-License-Identifier: AGPL-3.0-only
package provider

import (
	"context"
	"errors"
	"time"

	upstreamswu "github.com/boa-z/vowifi-go/engine/swu"
)

var ErrRekeyUnsupported = errors.New("upstream session does not expose CHILD-SA rekey")

// Forward the pinned implementation; this adapter owns no rekey policy or timer.
func (session *Session) rekeyScheduler() (upstreamswu.ChildSARekeyScheduler, error) {
	if session == nil || session.base == nil || session.closed.Load() {
		return nil, ErrProviderSessionClose
	}
	scheduler, ok := session.base.(upstreamswu.ChildSARekeyScheduler)
	if !ok {
		return nil, ErrRekeyUnsupported
	}
	return scheduler, nil
}

func (session *Session) NextChildSARekeyDue() (time.Time, bool) {
	scheduler, err := session.rekeyScheduler()
	if err != nil {
		return time.Time{}, false
	}
	return scheduler.NextChildSARekeyDue()
}

func (session *Session) ChildSARekeySnapshot() upstreamswu.ChildSARekeySnapshot {
	scheduler, err := session.rekeyScheduler()
	if err != nil {
		return upstreamswu.ChildSARekeySnapshot{}
	}
	return scheduler.ChildSARekeySnapshot()
}

func (session *Session) RekeyChildSA(ctx context.Context) (upstreamswu.TunnelResult, error) {
	scheduler, err := session.rekeyScheduler()
	if err != nil {
		return upstreamswu.TunnelResult{}, err
	}
	if ctx == nil {
		return upstreamswu.TunnelResult{}, errors.New("rekey context is required")
	}
	return scheduler.RekeyChildSA(ctx)
}

func (session *Session) RunChildSARekeyDue(ctx context.Context, now time.Time) (upstreamswu.ChildSARekeyDecision, error) {
	scheduler, err := session.rekeyScheduler()
	if err != nil {
		return upstreamswu.ChildSARekeyDecision{}, err
	}
	if ctx == nil {
		return upstreamswu.ChildSARekeyDecision{}, errors.New("rekey context is required")
	}
	return scheduler.RunChildSARekeyDue(ctx, now)
}

var _ upstreamswu.ChildSARekeyScheduler = (*Session)(nil)

func (session *Session) SupportsRekey() bool { _, err := session.rekeyScheduler(); return err == nil }

// MDD's old SWu loop retained the established SA after a failed proactive
// rekey and waited 300 seconds. The upstream scheduler owns the exchange and
// successful schedule advancement; this loop only supplies lifecycle timing.
func (session *Session) RunRekeyMaintenance(ctx context.Context) error {
	if ctx == nil {
		return errors.New("rekey maintenance context is required")
	}
	scheduler, err := session.rekeyScheduler()
	if errors.Is(err, ErrRekeyUnsupported) {
		return nil
	}
	if err != nil {
		return err
	}
	if !session.rekeyRunning.CompareAndSwap(false, true) {
		return errors.New("rekey maintenance already running")
	}
	defer session.rekeyRunning.Store(false)
	for {
		due, enabled := scheduler.NextChildSARekeyDue()
		if !enabled {
			return nil
		}
		session.rekeyMu.Lock()
		retryAt := session.rekeyRetryAt
		session.rekeyMu.Unlock()
		if retryAt.After(due) {
			due = retryAt
		}
		timer := time.NewTimer(max(time.Duration(0), time.Until(due)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if session.closed.Load() {
			return ErrProviderSessionClose
		}
		operation, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := scheduler.RunChildSARekeyDue(operation, time.Now())
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		session.rekeyMu.Lock()
		session.rekeyFailure = err
		if err != nil {
			session.rekeyRetryAt = time.Now().Add(5 * time.Minute)
		} else {
			session.rekeyRetryAt = time.Time{}
		}
		session.rekeyMu.Unlock()
		// A scheduler which returns success without advancing its due time must
		// not produce a CPU/network loop. Preserve the SA and delay observation.
		if next, ok := scheduler.NextChildSARekeyDue(); err == nil && ok && !next.After(time.Now()) {
			session.rekeyMu.Lock()
			session.rekeyFailure = errors.New("upstream rekey schedule did not advance")
			session.rekeyRetryAt = time.Now().Add(5 * time.Minute)
			session.rekeyMu.Unlock()
		}
	}
}

func (session *Session) RekeyMaintenanceStatus() (time.Time, error) {
	session.rekeyMu.Lock()
	defer session.rekeyMu.Unlock()
	return session.rekeyRetryAt, session.rekeyFailure
}
