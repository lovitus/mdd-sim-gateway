//go:build linux

package main

import (
	"context"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxmodem"
)

func newModemProber(options modemProberOptions) (agentmodem.Prober, error) {
	if !options.Enabled {
		return nil, nil
	}
	if options.ManagedRuntime {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		guard, err := linuxdataguard.Activate(ctx)
		if err != nil {
			return nil, err
		}
		return linuxmodem.NewManagedProber(options.SIMAPDU, guard, options.AgentID,
			options.RawRecovery, options.RecoveryOnly)
	}
	return linuxmodem.NewProber(options.SIMAPDU)
}
