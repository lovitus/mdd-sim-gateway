package runtimereconcile

import (
	"context"
	"time"
)

func (reconciler *Reconciler) scheduleDefaultDrafts() {
	if reconciler.reconcileDefaultDrafts == nil || reconciler.ctx.Err() != nil {
		return
	}
	reconciler.mu.Lock()
	if reconciler.defaultsInFlight || reconciler.now().Before(reconciler.defaultsNext) {
		reconciler.mu.Unlock()
		return
	}
	reconciler.defaultsInFlight = true
	reconciler.wg.Add(1)
	reconciler.mu.Unlock()
	go func() {
		defer reconciler.wg.Done()
		ctx, cancel := context.WithTimeout(reconciler.ctx, 3*time.Minute)
		err := reconciler.reconcileDefaultDrafts(ctx)
		cancel()
		reconciler.mu.Lock()
		delay := 30 * time.Second
		if err != nil {
			reconciler.defaultsFailures++
			delay = max(delay, reconciler.retryDelay(reconciler.defaultsFailures, err))
		} else {
			reconciler.defaultsFailures = 0
		}
		reconciler.defaultsNext = reconciler.now().Add(delay)
		reconciler.defaultsInFlight = false
		reconciler.mu.Unlock()
		if err != nil && reconciler.ctx.Err() == nil {
			reconciler.logf("initialize default drafts: %v", err)
		}
	}()
}
