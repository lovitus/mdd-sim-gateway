//go:build windows && (amd64 || arm64)

package main

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowsmbn"
)

func newModemProber(options modemProberOptions) (agentmodem.Prober, error) {
	if !options.Enabled {
		return nil, nil
	}
	return windowsmbn.NewProber(options.SIMAPDU, options.ManagedRuntime, options.AgentID,
		options.RawRecovery, options.RecoveryOnly)
}
