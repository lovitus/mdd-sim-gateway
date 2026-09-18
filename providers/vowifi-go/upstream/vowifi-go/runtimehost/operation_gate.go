package runtimehost

import (
	"context"
	"sync"
)

// operationGate serializes existing maintenance operations without making
// request cancellation wait behind another operation's network timeout.
// It is a lock, not a worker or a second recovery scheduler.
type operationGate struct {
	once  sync.Once
	token chan struct{}
}

func (g *operationGate) init() { g.once.Do(func() { g.token = make(chan struct{}, 1) }) }
func (g *operationGate) Lock() { _ = g.LockContext(context.Background()) }
func (g *operationGate) TryLock() bool {
	g.init()
	select {
	case g.token <- struct{}{}:
		return true
	default:
		return false
	}
}
func (g *operationGate) LockContext(ctx context.Context) error {
	g.init()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case g.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			g.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *operationGate) Unlock() { <-g.token }
