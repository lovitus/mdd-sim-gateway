//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestWindowsServiceStatusDoesNotRequireControlRights(t *testing.T) {
	if os.Getenv("MDD_GUI_SERVICE_TESTS") != "1" {
		t.Skip("requires isolated hosted Windows SCM")
	}
	manager, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Disconnect()
	if existing, err := manager.OpenService(windowsServiceName); err == nil {
		existing.Close()
		t.Fatal("will not touch an existing Agent service")
	} else if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	var output bytes.Buffer
	if err := runOSServiceWithExecutable("service-status", path, executable, config{}, &output); err != nil {
		t.Fatal(err)
	}
	var missing windowsServiceSnapshot
	if err := json.Unmarshal(output.Bytes(), &missing); err != nil || missing.Name != windowsServiceName ||
		missing.Platform != "windows-service" || missing.Installed || missing.State != "not_installed" {
		t.Fatalf("missing service status=%+v err=%v", missing, err)
	}
	installed, err := manager.CreateService(windowsServiceName, executable, mgr.Config{}, "service", "-config", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := installed.Delete(); err != nil {
			t.Error(err)
		}
		if err := installed.Close(); err != nil {
			t.Error(err)
		}
	}()
	// Only this new, never-started service is changed. Everyone can query it;
	// the test identity will have the administrator and SYSTEM SIDs deny-only.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x5;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetSecurityInfo(installed.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	var process, restricted, reader windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &process); err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	var disabled []windows.SIDAndAttributes
	for _, kind := range []windows.WELL_KNOWN_SID_TYPE{windows.WinBuiltinAdministratorsSid, windows.WinLocalSystemSid} {
		sid, err := windows.CreateWellKnownSid(kind)
		if err != nil {
			t.Fatal(err)
		}
		disabled = append(disabled, windows.SIDAndAttributes{Sid: sid})
	}
	created, _, createErr := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken").Call(
		uintptr(process), 1, uintptr(len(disabled)), uintptr(unsafe.Pointer(&disabled[0])), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	runtime.KeepAlive(disabled)
	if created == 0 {
		t.Fatal(createErr)
	}
	defer restricted.Close()
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &reader); err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.SetThreadToken(nil, reader); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			panic(err)
		}
	}()
	// Reopen every query under the restricted identity, including the old
	// path's broader SCM access, so denial cannot be blamed on that first step.
	queryManager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT|windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		t.Fatal("restricted SCM query unavailable", err)
	}
	defer windows.CloseServiceHandle(queryManager)
	name, _ := windows.UTF16PtrFromString(windowsServiceName)
	query, err := windows.OpenService(queryManager, name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		t.Fatal("restricted service query unavailable", err)
	}
	windows.CloseServiceHandle(query)
	for _, access := range []uint32{windows.SERVICE_START, windows.SERVICE_STOP} {
		handle, err := windows.OpenService(queryManager, name, access)
		if err == nil {
			windows.CloseServiceHandle(handle)
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("restricted control right %x: %v", access, err)
		}
	}
	output.Reset()
	if err := runOSServiceWithExecutable("service-status", path, executable, config{}, &output); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatal("SERVICE_STATUS_CONTROL_RIGHTS: query-only entry was denied after query rights were proven", err)
		}
		t.Fatal("query-only service status failed", err)
	}
	var snapshot windowsServiceSnapshot
	if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil || snapshot.Name != windowsServiceName ||
		snapshot.Platform != "windows-service" || !snapshot.Installed || snapshot.State != "stopped" {
		t.Fatalf("query-only service status=%+v err=%v", snapshot, err)
	}
}

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
