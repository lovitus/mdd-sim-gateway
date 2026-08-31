//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/kardianos/service"
)

type recordingServiceLogger struct{ messages []string }

func (logger *recordingServiceLogger) Error(values ...interface{}) error {
	logger.messages = append(logger.messages, fmt.Sprint(values...))
	return nil
}
func (logger *recordingServiceLogger) Warning(values ...interface{}) error {
	logger.messages = append(logger.messages, fmt.Sprint(values...))
	return nil
}
func (logger *recordingServiceLogger) Info(values ...interface{}) error {
	logger.messages = append(logger.messages, fmt.Sprint(values...))
	return nil
}
func (logger *recordingServiceLogger) Errorf(format string, values ...interface{}) error {
	return logger.Error(fmt.Sprintf(format, values...))
}
func (logger *recordingServiceLogger) Warningf(format string, values ...interface{}) error {
	return logger.Warning(fmt.Sprintf(format, values...))
}
func (logger *recordingServiceLogger) Infof(format string, values ...interface{}) error {
	return logger.Info(fmt.Sprintf(format, values...))
}

func TestWindowsServiceLogWriterPreservesBoundedDiagnostic(t *testing.T) {
	logger := new(recordingServiceLogger)
	payload := []byte(" raw USB capture failed\r\n")
	written, err := (windowsServiceLogWriter{logger: logger}).Write(payload)
	if err != nil || written != len(payload) || len(logger.messages) != 1 || logger.messages[0] != "raw USB capture failed" {
		t.Fatalf("written=%d messages=%q err=%v", written, logger.messages, err)
	}
}

func TestWindowsRawSourceServiceUsesOnlyDriverDependencyAndSCMRecovery(t *testing.T) {
	configuration := windowsServiceConfig(
		`C:\Program Files\MDD\mdd-agent.exe`,
		`C:\ProgramData\MDD\agent.json`,
		[]string{windowsRawDriverService},
	)
	if configuration.Name != windowsServiceName || configuration.Executable == "" ||
		len(configuration.Arguments) != 3 || configuration.Arguments[0] != "service" ||
		len(configuration.Dependencies) != 1 || configuration.Dependencies[0] != "VBoxUSBMon" {
		t.Fatalf("service configuration=%+v", configuration)
	}
	if serviceOptionString(configuration.Option, service.StartType) != service.ServiceStartAutomatic ||
		serviceOptionString(configuration.Option, service.OnFailure) != service.OnFailureRestart ||
		serviceOptionString(configuration.Option, service.OnFailureDelayDuration) != "5s" ||
		serviceOptionInt(configuration.Option, service.OnFailureResetPeriod) != windowsRecoveryResetSecs {
		t.Fatalf("service recovery options=%+v", configuration.Option)
	}
}

func TestWindowsRawSourceDependencyMustBeExact(t *testing.T) {
	if !exactWindowsRawDriverDependency([]string{"VBoxUSBMon"}) {
		t.Fatal("exact VBoxUSBMon dependency was rejected")
	}
	for _, dependencies := range [][]string{nil, {}, {"usbipd"}, {"VBoxUSBMon", "usbipd"}} {
		if exactWindowsRawDriverDependency(dependencies) {
			t.Fatalf("unsafe dependencies were accepted: %q", dependencies)
		}
	}
}

func TestWindowsUnexpectedWorkerExitIsFatalToSCMProcess(t *testing.T) {
	logger := new(recordingServiceLogger)
	exitCode := 0
	handleWindowsServiceUnexpectedExit(nil, logger, func(code int) { exitCode = code })
	if exitCode == 0 || len(logger.messages) != 1 || logger.messages[0] == "" {
		t.Fatalf("exitCode=%d messages=%q", exitCode, logger.messages)
	}
}

func TestWindowsUnexpectedWorkerExitHelper(t *testing.T) {
	if os.Getenv("MDD_TEST_FATAL_SERVICE_EXIT") != "1" {
		return
	}
	handleWindowsServiceUnexpectedExit(nil, nil, os.Exit)
	t.Fatal("fatal service exit returned")
}

func TestWindowsUnexpectedWorkerExitUsesNonzeroProcessStatus(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestWindowsUnexpectedWorkerExitHelper$")
	command.Env = append(os.Environ(), "MDD_TEST_FATAL_SERVICE_EXIT=1")
	err := command.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() == 0 {
		t.Fatalf("fatal helper error=%v", err)
	}
}

func serviceOptionString(values service.KeyValue, key string) string {
	value, _ := values[key].(string)
	return value
}

func serviceOptionInt(values service.KeyValue, key string) int {
	value, _ := values[key].(int)
	return value
}
