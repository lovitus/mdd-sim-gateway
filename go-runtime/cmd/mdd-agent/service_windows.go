//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kardianos/service"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowsdataguard"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	windowsServiceName       = "MDDAgent"
	windowsRawDriverService  = "VBoxUSBMon"
	windowsRecoveryResetSecs = 24 * 60 * 60
)

type windowsServiceProgram struct {
	settings config
	host     *managedHost
}

func (program *windowsServiceProgram) Start(current service.Service) error {
	if program.settings.Agent.RawUSBSource {
		if err := requireWindowsRawSourceServiceDependency(); err != nil {
			return err
		}
	}
	worker, err := buildWorker(program.settings)
	if err != nil {
		return err
	}
	logger, loggerErr := current.Logger(nil)
	if loggerErr != nil {
		log.Printf("mdd-agent service logger: %v", loggerErr)
	} else if logger != nil {
		// A Windows service has no inherited console. Route the existing bounded
		// Agent diagnostics into the service logger so recoverable hardware and
		// transport failures are not silently discarded while the SCM process
		// itself remains healthy.
		log.SetOutput(windowsServiceLogWriter{logger: logger})
	}
	ready := make(chan struct{})
	host, err := newManagedHost(func(ctx context.Context) error {
		return runHostWithReady(ctx, program.settings, worker, func() { close(ready) })
	}, func(runErr error) {
		if !program.host.readyAccepted() {
			return
		}
		handleWindowsServiceUnexpectedExit(runErr, logger, os.Exit)
	})
	if err != nil {
		return err
	}
	program.host = host
	if err := program.host.start(); err != nil {
		return err
	}
	if err := program.host.waitReady(ready, 5*time.Second); err != nil {
		return errors.Join(err, program.host.stop(serviceStopTimeout(program.settings)))
	}
	return nil
}

func handleWindowsServiceUnexpectedExit(runErr error, logger service.Logger, exit func(int)) {
	if runErr == nil {
		runErr = errors.New("MDD Agent host exited unexpectedly")
	}
	if logger != nil {
		_ = logger.Errorf("MDD Agent host stopped unexpectedly: %v", runErr)
	} else {
		log.Printf("mdd-agent host stopped unexpectedly: %v", runErr)
	}
	exit(1)
}

type windowsServiceLogWriter struct{ logger service.Logger }

func (writer windowsServiceLogWriter) Write(payload []byte) (int, error) {
	message := strings.TrimSpace(strings.ToValidUTF8(string(payload), "?"))
	if message == "" || writer.logger == nil {
		return len(payload), nil
	}
	_ = writer.logger.Info(message)
	return len(payload), nil
}

func (program *windowsServiceProgram) Stop(service.Service) error {
	if program.host == nil {
		return nil
	}
	return program.host.stop(serviceStopTimeout(program.settings))
}

type windowsServiceSnapshot struct {
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	Installed bool   `json:"installed"`
	State     string `json:"state"`
}

func runOSService(command, configPath string, settings config, output io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return runOSServiceWithExecutable(command, configPath, executable, settings, output)
}

func runOSServiceWithExecutable(command, configPath, executable string, settings config, output io.Writer) error {
	if !filepath.IsAbs(executable) {
		return errors.New("Windows service executable path must be absolute")
	}
	program := &windowsServiceProgram{settings: settings}
	var dependencies []string
	if settings.Agent.RawUSBSource {
		// The signed VBoxUSB driver owns the persistent PnP binding. The usbipd
		// user-mode TCP server is deliberately disabled and is not a runtime
		// dependency; MDD opens the forced device directly through VBoxUSBMon.
		dependencies = []string{windowsRawDriverService}
	}
	if command == "service-install" && settings.Agent.RawUSBSource {
		if err := rawusb.BeginWindowsPersistentSource(filepath.Dir(executable)); err != nil {
			return err
		}
		if err := prepareWindowsPersistentRawSource(filepath.Dir(executable)); err != nil {
			return err
		}
	}
	current, err := service.New(program, windowsServiceConfig(executable, configPath, dependencies))
	if err != nil {
		return err
	}
	switch command {
	case "service":
		return current.Run()
	case "service-install", "service-uninstall", "service-start", "service-stop":
		if command == "service-uninstall" {
			status, statusErr := current.Status()
			if statusErr != nil && !errors.Is(statusErr, service.ErrNotInstalled) {
				return statusErr
			}
			if status == service.StatusRunning {
				return errors.New("stop the MDD Agent service before uninstalling it")
			}
		}
		operation := command[len("service-"):]
		if err := service.Control(current, operation); err != nil {
			return err
		}
		if command == "service-install" {
			if err := configureWindowsServiceRecovery(); err != nil {
				uninstallErr := service.Control(current, "uninstall")
				return errors.Join(err, uninstallErr)
			}
		}
		return writeWindowsServiceStatus(current, output)
	case "service-status":
		return writeWindowsServiceStatus(current, output)
	default:
		return fmt.Errorf("unsupported service command %q", command)
	}
}

func prepareWindowsPersistentRawSource(packageDirectory string) error {
	guard, err := windowsdataguard.New()
	if err != nil {
		return fmt.Errorf("install persistent Windows data guard: %w", err)
	}
	if err := guard.Close(); err != nil {
		return fmt.Errorf("persist Windows data guard: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := rawusb.PrepareWindowsPersistentSource(ctx, packageDirectory); err != nil {
		return fmt.Errorf("secure usbipd driver installation: %w", err)
	}
	return nil
}

func windowsServiceConfig(executable, configPath string, dependencies []string) *service.Config {
	return &service.Config{
		Name:         windowsServiceName,
		DisplayName:  "MDD Agent",
		Description:  "MDD modem and smart card Agent",
		Executable:   executable,
		Arguments:    []string{"service", "-config", configPath},
		Dependencies: dependencies,
		Option: service.KeyValue{
			service.StartType:              service.ServiceStartAutomatic,
			service.OnFailure:              service.OnFailureRestart,
			service.OnFailureDelayDuration: "5s",
			service.OnFailureResetPeriod:   windowsRecoveryResetSecs,
		},
	}
}

func configureWindowsServiceRecovery() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("open Windows service manager: %w", err)
	}
	defer manager.Disconnect()
	current, err := manager.OpenService(windowsServiceName)
	if err != nil {
		return fmt.Errorf("open installed MDD Agent service: %w", err)
	}
	defer current.Close()
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	if err := current.SetRecoveryActions(actions, windowsRecoveryResetSecs); err != nil {
		return fmt.Errorf("configure MDD Agent recovery actions: %w", err)
	}
	if err := current.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("configure MDD Agent non-crash recovery: %w", err)
	}
	return nil
}

func writeWindowsServiceStatus(current service.Service, output io.Writer) error {
	status, err := current.Status()
	snapshot := windowsServiceSnapshot{Name: windowsServiceName, Platform: current.Platform(), Installed: true, State: "unknown"}
	if errors.Is(err, service.ErrNotInstalled) {
		snapshot.Installed = false
		snapshot.State = "not_installed"
		err = nil
	} else {
		switch status {
		case service.StatusRunning:
			snapshot.State = "running"
		case service.StatusStopped:
			snapshot.State = "stopped"
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(snapshot)
}
