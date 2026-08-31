//go:build !windows && !darwin && !linux

package main

import (
	"errors"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func newModemProber(options modemProberOptions) (agentmodem.Prober, error) {
	if !options.Enabled {
		return nil, nil
	}
	return nil, errors.New("modem management is currently available only on Windows, macOS, and Linux")
}
