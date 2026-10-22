//go:build gui && windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestGUIServiceTargetsOnlyItsOwnConfiguration(t *testing.T) {
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
	binary := filepath.Join(filepath.Dir(executable), "mdd-agent.exe")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := checkGUIServiceTarget(path, true); err != nil {
		t.Fatal("empty host refused installation", err)
	}
	if err := checkGUIServiceTarget(path, false); err == nil {
		t.Fatal("missing service admitted an operation")
	}
	service, err := manager.CreateService(windowsServiceName, binary, mgr.Config{}, "service", "-config", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := service.Delete(); err != nil {
			t.Error(err)
		}
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := checkGUIServiceTarget(path, false); err != nil {
		t.Fatal("exact owner refused", err)
	}
	if err := checkGUIServiceTarget(filepath.Join(t.TempDir(), "other.json"), false); err == nil {
		t.Fatal("GUI configuration B admitted service A")
	}
	if err := checkGUIServiceTarget(path, true); err == nil {
		t.Fatal("install would overwrite existing service")
	}
	installed, err := service.Config()
	if err != nil {
		t.Fatal(err)
	}
	installed.BinaryPathName = windows.ComposeCommandLine([]string{filepath.Join(filepath.Dir(binary), "other.exe"), "service", "-config", path})
	if err := service.UpdateConfig(installed); err != nil {
		t.Fatal(err)
	}
	if err := checkGUIServiceTarget(path, false); err == nil {
		t.Fatal("different service executable admitted")
	}
}
