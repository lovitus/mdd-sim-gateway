//go:build gui && (darwin || windows)

package main

import (
	"context"
	"errors"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

// The pointer belongs to this GUI's successfully bound host. Never resolve a
// lifecycle mutation through a reusable loopback address or numeric generation.
type guiRuntimeOwner struct {
	lifetime context.Context
	runtime  *agentcontrol.Controller
}

func (owner *guiRuntimeOwner) apply(ctx context.Context, observed agentcontrol.Snapshot, action string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner == nil || owner.lifetime.Err() != nil {
		return errors.New("原 Agent 宿主已结束；未操作替代进程")
	}
	current := owner.runtime.Status()
	if current.Generation != observed.Generation || current.State != observed.State || !current.ChangedAt.Equal(observed.ChangedAt) {
		return errors.New("Agent 状态已变化，请刷新后重新确认")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := owner.lifetime.Err(); err != nil {
		return err
	}
	var err error
	switch action {
	case "start":
		_, err = owner.runtime.Start(ctx)
	case "stop":
		_, err = owner.runtime.Stop(ctx)
	default:
		return errors.New("unsupported GUI runtime action")
	}
	return err
}
