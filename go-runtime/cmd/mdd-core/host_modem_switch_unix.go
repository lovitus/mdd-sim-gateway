//go:build !windows

package main

import (
	"context"
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerdeploy"
)

type hostModemServices interface {
	HostModemServiceState(context.Context, string) (providerdeploy.HostServiceState, error)
	HostModemAction(context.Context, string, string) error
}

// Source: ec620942 host/mdd_orchestrator.py:set_cellular_backend. The caller
// owns the mutation lock, exact Agent binding, idle admission and config backup.
func switchHostModemServices(ctx context.Context, backend string, manager hostModemServices, restoreConfig func() error, verifyAgent func(context.Context) error) (string, error) {
	if manager == nil || restoreConfig == nil || verifyAgent == nil || (backend != "auto" && backend != "serial") {
		return "not_applied", errors.New("host modem switch dependencies unavailable")
	}
	agent, err := manager.HostModemServiceState(ctx, "mdd-agent.service")
	if err != nil {
		return "not_applied", err
	}
	if agent.ActiveState != "active" || agent.MainPID == 0 {
		return "not_applied", errors.New("host Agent is not active")
	}
	mm, err := manager.HostModemServiceState(ctx, "ModemManager.service")
	if err != nil {
		return "not_applied", err
	}
	absent := mm.LoadState == "not-found"
	masked := mm.UnitFileState == "masked"
	if (!absent && mm.ActiveState != "active" && mm.ActiveState != "inactive") ||
		(!absent && mm.UnitFileState != "enabled" && mm.UnitFileState != "disabled" && !masked) ||
		(backend == "auto" && (absent || masked)) || (masked && mm.ActiveState != "inactive") {
		return "not_applied", errors.New("ModemManager state does not permit requested switch")
	}
	action := func(action, unit string) error { return manager.HostModemAction(ctx, action, unit) }
	apply := func() error {
		if err := action("stop", "mdd-agent.service"); err != nil {
			return err
		}
		if backend == "serial" {
			if !absent && !masked {
				if mm.ActiveState == "active" {
					if err := action("stop", "ModemManager.service"); err != nil {
						return err
					}
				}
				if mm.UnitFileState == "enabled" {
					if err := action("disable", "ModemManager.service"); err != nil {
						return err
					}
				}
			}
		} else {
			if mm.UnitFileState != "enabled" {
				if err := action("enable", "ModemManager.service"); err != nil {
					return err
				}
			}
			if mm.ActiveState != "active" {
				if err := action("start", "ModemManager.service"); err != nil {
					return err
				}
			}
		}
		if err := action("start", "mdd-agent.service"); err != nil {
			return err
		}
		return verifyAgent(ctx)
	}
	if err := apply(); err != nil {
		rollback, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		stopErr := manager.HostModemAction(rollback, "stop", "mdd-agent.service")
		if stopErr != nil {
			return "recovery_required", errors.Join(err, stopErr)
		}
		if restoreErr := restoreConfig(); restoreErr != nil {
			return "recovery_required", errors.Join(err, restoreErr)
		}
		var failures []error
		if !absent && !masked {
			enableAction := "disable"
			if mm.UnitFileState == "enabled" {
				enableAction = "enable"
			}
			failures = append(failures, manager.HostModemAction(rollback, enableAction, "ModemManager.service"))
			activeAction := "stop"
			if mm.ActiveState == "active" {
				activeAction = "start"
			}
			failures = append(failures, manager.HostModemAction(rollback, activeAction, "ModemManager.service"))
		}
		if restoreErr := errors.Join(failures...); restoreErr != nil {
			return "recovery_required", errors.Join(err, restoreErr)
		}
		if startErr := manager.HostModemAction(rollback, "start", "mdd-agent.service"); startErr != nil {
			return "recovery_required", errors.Join(err, startErr)
		}
		if readyErr := verifyAgent(rollback); readyErr != nil {
			return "recovery_required", errors.Join(err, readyErr)
		}
		return "rolled_back", err
	}
	return "applied", nil
}
