//go:build darwin

package main

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/darwinmodem"
)

func newModemProber(options modemProberOptions) (agentmodem.Prober, error) {
	if !options.Enabled {
		return nil, nil
	}
	if options.ManagedRuntime {
		if err := requestMicrophoneAuthorization(); err != nil {
			return nil, err
		}
	}
	return darwinmodem.NewProber(options.SIMAPDU)
}
