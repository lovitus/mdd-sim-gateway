//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func supervisedRestartAvailable(hostMode string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if runtime.GOOS == "linux" && hostMode == "service" {
		output, err := exec.CommandContext(ctx, "systemctl", "show", "mdd-agent.service", "--property=MainPID", "--value").Output()
		return err == nil && strings.TrimSpace(string(output)) == strconv.Itoa(os.Getpid())
	}
	if runtime.GOOS == "darwin" && hostMode == "gui" {
		output, err := exec.CommandContext(ctx, "/bin/launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/com.mdd.agent").Output()
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[0] == "pid" && fields[1] == "=" && fields[2] == strconv.Itoa(os.Getpid()) {
				return true
			}
		}
	}
	return false
}

func runOSService(command, _ string, _ config, output io.Writer) error {
	if command == "service-restart" {
		name, args, err := managedRestartCommand(runtime.GOOS, os.Getuid())
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, name, args...).Run(); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]string{"state": "restart_requested"})
	}
	return errors.New("OS service management is available only on Windows; run the Agent host or GUI directly on this platform")
}

func managedRestartCommand(platform string, uid int) (string, []string, error) {
	switch platform {
	case "linux":
		return "systemctl", []string{"--no-block", "restart", "mdd-agent.service"}, nil
	case "darwin":
		if uid < 0 {
			return "", nil, errors.New("unknown launchd user domain")
		}
		return "/bin/launchctl", []string{"kickstart", "-k", "gui/" + strconv.Itoa(uid) + "/com.mdd.agent"}, nil
	default:
		return "", nil, errors.New("managed Agent restart is unsupported on this platform")
	}
}

func runOSServiceWithExecutable(string, string, string, config, io.Writer) error {
	return errors.New("OS service management is available only on Windows; run the Agent host or GUI directly on this platform")
}
