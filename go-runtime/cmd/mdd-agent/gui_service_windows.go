//go:build gui && windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func checkGUIServiceTarget(path string, installing bool) error {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(manager)
	name, _ := windows.UTF16PtrFromString(windowsServiceName)
	handle, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_CONFIG)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) && installing {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取现有 Agent 服务失败，未执行操作：%w", err)
	}
	current := &mgr.Service{Name: windowsServiceName, Handle: handle}
	defer current.Close()
	if installing {
		return errors.New("Agent 服务已存在；不会覆盖安装或改变其配置")
	}
	installed, err := current.Config()
	if err != nil {
		return err
	}
	arguments, err := windows.DecomposeCommandLine(installed.BinaryPathName)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	expected := filepath.Join(filepath.Dir(executable), "mdd-agent.exe")
	if len(arguments) != 4 || !strings.EqualFold(filepath.Clean(arguments[0]), filepath.Clean(expected)) ||
		arguments[1] != "service" || arguments[2] != "-config" || !strings.EqualFold(filepath.Clean(arguments[3]), filepath.Clean(path)) {
		return errors.New("现有 Windows 服务使用不同的程序或配置；未操作该服务，请打开与服务匹配的 Agent")
	}
	return nil
}
