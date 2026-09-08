//go:build linux

package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
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
		if options.Backend == "serial" {
			if err := linuxmodem.ValidateSerialProfiles(options.Profiles); err != nil {
				return nil, err
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		guard, err := linuxdataguard.Activate(ctx)
		if err != nil {
			return nil, err
		}
		if options.Backend == "serial" {
			if err := ensureModemManagerStopped(ctx); err != nil {
				return nil, err
			}
			return linuxmodem.NewManagedSerialProber(options.SIMAPDU, options.Profiles, guard, options.AgentID, options.RawRecovery, options.RecoveryOnly, ensureModemManagerStopped)
		}
		return linuxmodem.NewManagedProber(options.SIMAPDU, guard, options.AgentID,
			options.RawRecovery, options.RecoveryOnly)
	}
	return linuxmodem.NewProber(options.SIMAPDU)
}

func ensureModemManagerStopped(ctx context.Context) error {
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(check, "systemctl", "show", "ModemManager.service", "--property=LoadState", "--property=ActiveState", "--property=UnitFileState").Output()
	if err != nil {
		return errors.New("cannot verify ModemManager ownership for serial mode")
	}
	return validateSerialServiceState(string(output))
}

func validateSerialServiceState(output string) error {
	values := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = value
		}
	}
	if values["LoadState"] == "not-found" {
		return nil
	}
	if values["ActiveState"] == "inactive" && (values["UnitFileState"] == "disabled" || values["UnitFileState"] == "masked") {
		return nil
	}
	return errors.New("serial mode requires ModemManager stopped and disabled")
}
