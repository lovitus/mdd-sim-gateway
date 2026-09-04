package providerdeploy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type ServiceManager interface {
	IsActive(context.Context, string) (bool, error)
	IsEnabled(context.Context, string) (bool, error)
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Enable(context.Context, string) error
	Disable(context.Context, string) error
}

type Systemctl struct{ Path string }

func (manager Systemctl) Validate() error {
	if runtime.GOOS != "linux" {
		return errors.New("systemctl provider deployment is supported only on Linux")
	}
	path := filepath.Clean(strings.TrimSpace(manager.Path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return errors.New("systemctl path must be absolute and scoped")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("systemctl must be a non-writable executable regular file")
	}
	uid, _, ok := owner(info)
	if !ok || uid != 0 {
		return errors.New("systemctl must be owned by root")
	}
	return nil
}

func (manager Systemctl) IsActive(ctx context.Context, unit string) (bool, error) {
	return manager.query(ctx, "is-active", unit)
}

func (manager Systemctl) IsEnabled(ctx context.Context, unit string) (bool, error) {
	return manager.query(ctx, "is-enabled", unit)
}

func (manager Systemctl) Start(ctx context.Context, unit string) error {
	return manager.action(ctx, "start", unit)
}
func (manager Systemctl) Stop(ctx context.Context, unit string) error {
	return manager.action(ctx, "stop", unit)
}

// RestartFixed restarts only the three server-owned fixed units. Provider
// instance units are deliberately excluded; maintenance drain owns them.
func (manager Systemctl) RestartFixed(ctx context.Context, unit string) error {
	unit = strings.TrimSpace(unit)
	switch unit {
	case "mdd-core.service", "mdd-provider-apply.service", "mdd-egress.service":
		return manager.run(ctx, "restart", unit)
	default:
		return errors.New("refusing to restart a non-fixed MDD unit")
	}
}
func (manager Systemctl) Enable(ctx context.Context, unit string) error {
	return manager.action(ctx, "enable", unit)
}
func (manager Systemctl) Disable(ctx context.Context, unit string) error {
	return manager.action(ctx, "disable", unit)
}

func (manager Systemctl) DaemonReload(ctx context.Context) error {
	return manager.run(ctx, "daemon-reload")
}

func (manager Systemctl) query(ctx context.Context, action, unit string) (bool, error) {
	unit, err := checkedUnit(unit)
	if err != nil {
		return false, err
	}
	err = manager.run(ctx, action, "--quiet", unit)
	if err == nil {
		return true, nil
	}
	var commandError *CommandError
	if errors.As(err, &commandError) && (commandError.ExitCode == 1 || commandError.ExitCode == 3 || commandError.ExitCode == 4) {
		return false, nil
	}
	return false, err
}

func (manager Systemctl) action(ctx context.Context, action, unit string) error {
	unit, err := checkedUnit(unit)
	if err != nil {
		return err
	}
	return manager.run(ctx, action, unit)
}

func (manager Systemctl) run(ctx context.Context, arguments ...string) error {
	command := exec.CommandContext(ctx, manager.Path, arguments...)
	command.Env = []string{"LC_ALL=C", "SYSTEMD_COLORS=0"}
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		exitCode := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			exitCode = exit.ExitCode()
		}
		return &CommandError{ExitCode: exitCode, Output: strings.TrimSpace(output.String())}
	}
	return nil
}

type CommandError struct {
	ExitCode int
	Output   string
}

func (failure *CommandError) Error() string { return "systemctl command failed" }

type limitedBuffer struct{ bytes.Buffer }

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if remaining := (16 << 10) - buffer.Len(); remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.Buffer.Write(value)
	}
	return original, nil
}

func checkedUnit(unit string) (string, error) {
	unit = strings.TrimSpace(unit)
	if !strings.HasPrefix(unit, "mdd-vowifi@line-") || !strings.HasSuffix(unit, ".service") ||
		strings.ContainsAny(unit, "/\\ \t\r\n") {
		return "", errors.New("invalid provider systemd unit")
	}
	return unit, nil
}
