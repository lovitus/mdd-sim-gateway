//go:build windows && !(amd64 || arm64)

package main

import (
	"errors"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func newModemProber(enabled bool) (agentmodem.Prober, error) {
	if !enabled {
		return nil, nil
	}
	return nil, errors.New("modem management requires Windows amd64 or arm64")
}
