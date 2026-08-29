//go:build !windows

package main

import (
	"errors"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func newModemProber(enabled, _, _ bool) (agentmodem.Prober, error) {
	if !enabled {
		return nil, nil
	}
	return nil, errors.New("modem management is currently available only on Windows")
}
