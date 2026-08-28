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
	"time"

	"github.com/kardianos/service"
)

const windowsServiceName = "MDDAgent"

type windowsServiceProgram struct {
	settings config
	host     *managedHost
}

func (program *windowsServiceProgram) Start(current service.Service) error {
	worker, err := buildWorker(program.settings)
	if err != nil {
		return err
	}
	logger, loggerErr := current.Logger(nil)
	if loggerErr != nil {
		log.Printf("mdd-agent service logger: %v", loggerErr)
	}
	ready := make(chan struct{})
	host, err := newManagedHost(func(ctx context.Context) error {
		return runHostWithReady(ctx, program.settings, worker, func() { close(ready) })
	}, func(runErr error) {
		if !program.host.readyAccepted() {
			return
		}
		if runErr != nil {
			if logger != nil {
				_ = logger.Errorf("MDD Agent host stopped unexpectedly: %v", runErr)
			} else {
				log.Printf("mdd-agent host stopped unexpectedly: %v", runErr)
			}
		}
		if stopErr := current.Stop(); stopErr != nil {
			log.Printf("mdd-agent service self-stop: %v", stopErr)
		}
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
	program := &windowsServiceProgram{settings: settings}
	current, err := service.New(program, &service.Config{
		Name:        windowsServiceName,
		DisplayName: "MDD Agent",
		Description: "MDD PC/SC card Agent",
		Executable:  executable,
		Arguments:   []string{"service", "-config", configPath},
		Option: service.KeyValue{
			service.StartType: service.ServiceStartAutomatic,
			service.OnFailure: service.OnFailureNoAction,
		},
	})
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
		if err := service.Control(current, command[len("service-"):]); err != nil {
			return err
		}
		return writeWindowsServiceStatus(current, output)
	case "service-status":
		return writeWindowsServiceStatus(current, output)
	default:
		return fmt.Errorf("unsupported service command %q", command)
	}
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
