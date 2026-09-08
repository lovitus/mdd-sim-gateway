package providerdeploy

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

func checkedHostModemUnit(unit string) error {
	if unit != "mdd-agent.service" && unit != "ModemManager.service" {
		return errors.New("invalid host modem service")
	}
	return nil
}

type HostServiceState struct {
	LoadState     string `json:"load_state"`
	ActiveState   string `json:"active_state"`
	UnitFileState string `json:"unit_file_state"`
	MainPID       uint64 `json:"main_pid"`
}

func (manager Systemctl) HostModemServiceState(ctx context.Context, unit string) (HostServiceState, error) {
	if err := checkedHostModemUnit(unit); err != nil {
		return HostServiceState{}, err
	}
	output, err := manager.output(ctx, "show", unit, "--property=LoadState", "--property=ActiveState", "--property=UnitFileState", "--property=MainPID")
	if err != nil {
		return HostServiceState{}, err
	}
	return parseHostServiceState(output)
}

func parseHostServiceState(output string) (HostServiceState, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			if _, exists := fields[key]; exists {
				return HostServiceState{}, errors.New("duplicate service state property")
			}
			fields[key] = value
		}
	}
	pid, err := strconv.ParseUint(fields["MainPID"], 10, 64)
	if err != nil || fields["LoadState"] == "" || fields["ActiveState"] == "" {
		return HostServiceState{}, errors.New("incomplete host service state")
	}
	return HostServiceState{LoadState: fields["LoadState"], ActiveState: fields["ActiveState"], UnitFileState: fields["UnitFileState"], MainPID: pid}, nil
}

// HostModemAction reuses the existing bounded command runner, without relaxing
// the provider-instance allowlist or exposing arbitrary systemd commands.
func (manager Systemctl) HostModemAction(ctx context.Context, action, unit string) error {
	if err := checkedHostModemUnit(unit); err != nil {
		return err
	}
	switch action {
	case "start", "stop", "enable", "disable":
	default:
		return errors.New("invalid host modem service action")
	}
	return manager.run(ctx, action, unit)
}
