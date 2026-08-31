//go:build windows && !(amd64 || arm64)

package main

import (
	"errors"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func newModemProber(options modemProberOptions) (agentmodem.Prober, error) {
	if !options.Enabled {
		return nil, nil
	}
	return nil, errors.New("modem management requires Windows amd64 or arm64")
}
