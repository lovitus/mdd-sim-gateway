//go:build linux

package main

import (
	"context"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
)

func runCellularGuardCommand(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: mdd-agent cellular-guard <apply|protect-netdev>")
	}
	switch arguments[0] {
	case "apply":
		if len(arguments) != 1 {
			return errors.New("cellular-guard apply accepts no arguments")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return linuxdataguard.Apply(ctx)
	case "protect-netdev":
		if len(arguments) != 2 {
			return errors.New("cellular-guard protect-netdev requires one interface name")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return linuxdataguard.ProtectNetdev(ctx, arguments[1])
	default:
		return errors.New("unknown cellular-guard command")
	}
}
