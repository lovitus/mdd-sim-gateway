//go:build gui && windows

package main

import (
	"context"
	"errors"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

func showInstalledServiceGUI(ctx context.Context, window fyne.Window, requested string, start func(config, string, guiServiceClient)) bool {
	path, pathErr := explicitOrEnvironmentPath(requested)
	if pathErr != nil {
		window.SetContent(container.NewPadded(guiStatus("指定配置路径无效；-config / MDD_AGENT_CONFIG 必须是绝对文件路径。未打开其他身份。", widget.WarningImportance)))
		return true
	}
	installed, err := installedManagementService()
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false
	}
	status := guiStatus("正在连接本机 Agent 服务", widget.WarningImportance)
	var refresh *widget.Button
	refresh = widget.NewButtonWithIcon("重新读取服务", theme.ViewRefreshIcon(), func() { showInstalledServiceGUI(ctx, window, requested, start) })
	window.SetContent(container.NewPadded(container.NewVBox(guiLabel("MDD Agent", true), status, refresh)))
	if err != nil {
		status.SetText("无法确认原服务安装目标；请由管理员检查。没有读取秘密配置或创建新 Agent。")
		return true
	}
	if path != "" && !sameWindowsPath(path, installed.path) {
		status.SetText("指定配置与现有服务不一致；不会接管其他身份。请打开原安装的 Agent。")
		return true
	}
	if installed.state != svc.Running {
		status.SetText("原 Windows 服务尚未运行（SCM 状态 " + fmt.Sprint(installed.state) + "）；请由管理员按原维护流程启动。未新建身份或启动硬件。")
		return true
	}
	client := &windowsManagementClient{installation: installed}
	refresh.Disable()
	go func() {
		state, err := client.request(ctx, nil)
		fyne.Do(func() {
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				status.SetText("原服务正在运行；本机管理连接未成功。\n" + err.Error())
				refresh.Enable()
				return
			}
			start(state.Running.guiConfig(), installed.path, client)
		})
	}()
	return true
}
