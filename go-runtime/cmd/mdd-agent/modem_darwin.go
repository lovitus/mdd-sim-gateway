//go:build darwin

package main

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/darwinmodem"
)

func newModemProber(enabled, simAPDU, managedRuntime bool) (agentmodem.Prober, error) {
	if !enabled {
		return nil, nil
	}
	if managedRuntime {
		if err := requestMicrophoneAuthorization(); err != nil {
			return nil, err
		}
	}
	return darwinmodem.NewProber(simAPDU)
}
