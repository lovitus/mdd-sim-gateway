//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerdeploy"
)

type hostModeReceipt struct {
	SchemaVersion int                        `json:"schema_version"`
	State         string                     `json:"state"`
	Revision      string                     `json:"revision"`
	LeaseID       string                     `json:"lease_id"`
	AgentID       string                     `json:"agent_id"`
	Held          bool                       `json:"held"`
	Providers     providerapply.DrainRequest `json:"providers"`
}

func readHostModeReceipt(path string) (hostModeReceipt, error) {
	var receipt hostModeReceipt
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return receipt, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return receipt, errors.New("host mode recovery record unavailable")
	}
	payload, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(payload, &receipt) != nil || receipt.SchemaVersion != 1 {
		return receipt, errors.New("host mode recovery record invalid")
	}
	return receipt, nil
}

func writeHostModeReceipt(path string, info os.FileInfo, receipt hostModeReceipt) error {
	payload, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return writeWebConfig(path, append(payload, '\n'), info)
}

// The caller holds the existing helper mutation lock for the whole operation.
func (service *providerApplyService) applyHostModemSettings(ctx context.Context, binding *provideradmin.HostModemBinding, input provideradmin.HostModemRequest) (hostModemSnapshot, error) {
	service.hostSwitching.Store(true)
	defer service.hostSwitching.Store(false)
	if err := validateHostModemSettings(input.Settings); err != nil {
		return hostModemSnapshot{}, err
	}
	previous, info, current, err := readHostModemConfig(binding.ConfigPath, binding.AgentID, binding.CoreURL)
	if err != nil {
		return current, err
	}
	if current.Revision != input.ExpectedRevision {
		return current, &provideradmin.Error{Status: 412, Code: "host_configuration_changed"}
	}
	recordPath := binding.ConfigPath + ".host-mode-status.json"
	oldRecord, err := readHostModeReceipt(recordPath)
	if err != nil {
		return current, err
	}
	if oldRecord.Held {
		return current, &provideradmin.Error{Status: 409, Code: "host_mode_recovery_required"}
	}
	a, _ := json.Marshal(current.Settings)
	b, _ := json.Marshal(input.Settings)
	if bytes.Equal(a, b) {
		current.RuntimeState = "not_observed"
		return current, nil
	}
	manager := providerdeploy.Systemctl{Path: service.settings.ProviderApply.SystemctlPath}
	if err := manager.Validate(); err != nil {
		return current, err
	}
	state, err := manager.HostModemServiceState(ctx, "mdd-agent.service")
	if err != nil {
		return current, err
	}
	if err := verifyHostAgentProcess("/proc", state, binding.ConfigPath); err != nil {
		return current, err
	}
	if err := verifyHostAgentReady(ctx, previous); err != nil {
		return current, err
	}
	operation, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	lease, err := service.acquireHostModeLease(operation, binding.AgentID)
	if err != nil {
		if lease != nil {
			record := hostModeReceipt{SchemaVersion: 1, State: "recovery_required", Revision: current.Revision, LeaseID: lease.id, AgentID: binding.AgentID, Held: true, Providers: lease.providers}
			err = errors.Join(err, writeHostModeReceipt(recordPath, info, record))
		}
		return current, err
	}
	record := hostModeReceipt{SchemaVersion: 1, State: "switching", Revision: current.Revision, LeaseID: lease.id, AgentID: binding.AgentID, Held: true, Providers: lease.providers}
	release := func() error {
		clean, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		return lease.release(clean)
	}
	if err := writeHostModeReceipt(recordPath, info, record); err != nil {
		return current, errors.Join(err, release())
	}
	saved, saveErr := saveHostModemConfig(binding.ConfigPath, binding.AgentID, binding.CoreURL, input.ExpectedRevision, input.Settings)
	if saveErr != nil {
		releaseErr := release()
		record.State = "not_applied"
		record.Held = releaseErr != nil
		if record.Held {
			record.State = "recovery_required"
		}
		return saved, errors.Join(saveErr, releaseErr, writeHostModeReceipt(recordPath, info, record))
	}
	record.Revision = saved.Revision
	restore := func() error {
		_, _, latest, err := readHostModemConfig(binding.ConfigPath, binding.AgentID, binding.CoreURL)
		if err != nil || latest.Revision != saved.Revision {
			return errors.New("configuration changed before rollback")
		}
		return writeWebConfig(binding.ConfigPath, previous, info)
	}
	verify := func(ctx context.Context) error {
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		state, err := manager.HostModemServiceState(ctx, "mdd-agent.service")
		if err != nil {
			return err
		}
		if err := verifyHostAgentProcess("/proc", state, binding.ConfigPath); err != nil {
			return err
		}
		payload, _, _, err := readHostModemConfig(binding.ConfigPath, binding.AgentID, binding.CoreURL)
		if err != nil {
			return err
		}
		return verifyHostAgentReady(ctx, payload)
	}
	result, switchErr := switchHostModemServices(operation, input.Settings.Backend, manager, restore, verify)
	if result == "not_applied" {
		if err := restore(); err != nil {
			result = "recovery_required"
			switchErr = errors.Join(switchErr, err)
		} else {
			result = "rolled_back"
		}
	}
	if result != "recovery_required" {
		if err := release(); err != nil {
			result = "recovery_required"
			switchErr = errors.Join(switchErr, err)
		} else {
			record.Held = false
		}
	}
	record.State = result
	_, _, actual, readErr := readHostModemConfig(binding.ConfigPath, binding.AgentID, binding.CoreURL)
	if readErr == nil {
		record.Revision = actual.Revision
	}
	actual.RuntimeState = result
	recordErr := writeHostModeReceipt(recordPath, info, record)
	if err := errors.Join(switchErr, readErr, recordErr); err != nil {
		return actual, &provideradmin.Error{Status: 409, Code: "host_mode_" + result, Cause: err}
	}
	return actual, nil
}
