//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func validateRawSourceServiceEnablement() error {
	installed, valid, err := windowsRawSourceServiceDependency()
	if err != nil {
		return err
	}
	if installed && !valid {
		return errors.New("uninstall the existing MDD Agent service before enabling raw USB, then install it again so SCM can add the VBoxUSBMon dependency")
	}
	return nil
}

func requireWindowsRawSourceServiceDependency() error {
	installed, valid, err := windowsRawSourceServiceDependency()
	if err != nil {
		return err
	}
	if !installed || !valid {
		return errors.New("raw USB source requires the installed MDD Agent service to depend on VBoxUSBMon")
	}
	return nil
}

func windowsRawSourceServiceDependency() (bool, bool, error) {
	managerHandle, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, false, fmt.Errorf("open Windows service manager: %w", err)
	}
	defer windows.CloseServiceHandle(managerHandle)
	serviceName, err := windows.UTF16PtrFromString(windowsServiceName)
	if err != nil {
		return false, false, err
	}
	serviceHandle, err := windows.OpenService(managerHandle, serviceName, windows.SERVICE_QUERY_CONFIG)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("query installed MDD Agent service: %w", err)
	}
	current := &mgr.Service{Name: windowsServiceName, Handle: serviceHandle}
	defer current.Close()
	configuration, err := current.Config()
	if err != nil {
		return true, false, err
	}
	return true, exactWindowsRawDriverDependency(configuration.Dependencies), nil
}

func exactWindowsRawDriverDependency(dependencies []string) bool {
	return len(dependencies) == 1 && strings.EqualFold(dependencies[0], windowsRawDriverService)
}
