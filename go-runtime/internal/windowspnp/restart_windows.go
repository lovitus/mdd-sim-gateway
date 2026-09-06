//go:build windows

package windowspnp

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func ResolveRestartTarget(ctx context.Context, attachmentID string) (string, error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return "", ErrRestartUnavailable
	}
	queryContext, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	payload, err := exec.CommandContext(queryContext, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"Get-CimInstance Win32_NetworkAdapter | Select-Object GUID,PNPDeviceID | ConvertTo-Json -Compress").Output()
	if err != nil {
		return "", ErrRestartUnavailable
	}
	target, err := parseRestartTarget(payload, attachmentID)
	if err != nil {
		return "", err
	}
	helpContext, stopHelp := context.WithTimeout(ctx, 8*time.Second)
	defer stopHelp()
	help, helpErr := exec.CommandContext(helpContext, "pnputil.exe", "/?").CombinedOutput()
	if helpErr != nil || !strings.Contains(strings.ToLower(string(help)), "/restart-device") {
		return "", ErrRestartUnavailable
	}
	return target, nil
}

func RestartDevice(ctx context.Context, target string) error {
	if target == "" || strings.ContainsAny(target, "\r\n\x00") {
		return ErrRestartUnavailable
	}
	restartContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(restartContext, "pnputil.exe", "/restart-device", target).CombinedOutput(); err != nil {
		return errors.New("pnputil restart-device failed: " + strings.TrimSpace(string(output)))
	}
	return nil
}
