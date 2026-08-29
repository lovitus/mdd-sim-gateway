//go:build windows && (amd64 || arm64)

package main

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowsmbn"
)

func newModemProber(enabled, simAPDU, protectData bool) (agentmodem.Prober, error) {
	if !enabled {
		return nil, nil
	}
	return windowsmbn.NewProber(simAPDU, protectData)
}
