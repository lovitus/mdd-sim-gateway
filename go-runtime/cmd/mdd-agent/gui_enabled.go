//go:build gui && (darwin || windows)

package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

//go:embed assets/mdd-agent.svg
var guiIcon []byte

type guiController struct {
	settings       config
	configPath     string
	window         fyne.Window
	summary        *widget.Label
	connection     *widget.Label
	service        *widget.Label
	details        *widget.Entry
	ctx            context.Context
	refresh        chan struct{}
	lastRender     string
	devices        *fyne.Container
	savedInfo      *widget.Label
	savedStatus    *widget.Label
	actionStatus   *widget.Label
	diagnosticText string
	actionButtons  []*widget.Button
	actionBusy     bool
	observed       *agentcontrol.Snapshot
}

type guiHostResult struct {
	host *managedHost
	err  error
}

func defaultLaunchCommand() string { return "gui" }

func runGUI(settings config, configPath string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	application := app.NewWithID("com.mdd.agent")
	application.Settings().SetTheme(agentTheme{Theme: theme.DefaultTheme()})
	icon := fyne.NewStaticResource("mdd-agent.svg", guiIcon)
	application.SetIcon(icon)
	window := application.NewWindow("MDD Agent")
	window.SetIcon(icon)
	window.Resize(fyne.NewSize(820, 640))
	hostResult := make(chan guiHostResult, 1)
	started := false
	start := func(ready config, path string) {
		if started || ctx.Err() != nil {
			return
		}
		started = true
		settings, configPath = ready, path
		controller := &guiController{
			settings: settings, configPath: configPath, window: window,
			ctx: ctx, refresh: make(chan struct{}, 1),
		}
		window.SetContent(controller.content())
		window.SetCloseIntercept(window.Hide)
		quit := func() {
			cancel()
			application.Quit()
		}
		if desk, ok := application.(desktop.App); ok {
			quitLabel := "退出 GUI（Agent 服务继续运行）"
			if runtime.GOOS == "darwin" {
				quitLabel = "退出 MDD Agent"
			}
			desk.SetSystemTrayIcon(icon)
			desk.SetSystemTrayWindow(window)
			desk.SetSystemTrayMenu(fyne.NewMenu("MDD Agent",
				fyne.NewMenuItem("打开 MDD Agent", window.Show),
				fyne.NewMenuItem("刷新", controller.requestRefresh),
				fyne.NewMenuItemSeparator(),
				fyne.NewMenuItem(quitLabel, quit),
			))
		}
		if runtime.GOOS == "darwin" {
			go func() {
				host, err := startGUIHost(settings)
				if ctx.Err() != nil && host != nil {
					_ = host.stop(serviceStopTimeout(settings))
					host = nil
				}
				hostResult <- guiHostResult{host: host, err: err}
				if err != nil && ctx.Err() == nil {
					fyne.Do(func() {
						if ctx.Err() == nil {
							dialog.ShowError(err, window)
						}
					})
				}
				controller.requestRefresh()
			}()
		}
		go controller.refreshLoop()
		controller.requestRefresh()
	}
	window.SetCloseIntercept(func() { cancel(); application.Quit() })
	application.Lifecycle().SetOnStarted(func() {
		fyne.Do(func() { showGUISetup(ctx, window, configPath, start) })
	})
	window.ShowAndRun()
	cancel()
	if runtime.GOOS == "darwin" && started {
		select {
		case result := <-hostResult:
			if result.host != nil {
				return result.host.stop(serviceStopTimeout(settings))
			}
			return result.err
		case <-time.After(serviceStopTimeout(settings) + 6*time.Second):
			return errors.New("Agent startup did not finish before GUI shutdown")
		}
	}
	return nil
}

func startGUIHost(settings config) (*managedHost, error) {
	worker, err := buildWorker(settings, "gui")
	if err != nil {
		return nil, err
	}
	ready := make(chan struct{})
	host, err := newManagedHost(func(ctx context.Context) error {
		return runHostWithReady(ctx, settings, worker, func() { close(ready) })
	}, nil)
	if err != nil {
		return nil, err
	}
	if err := host.start(); err != nil {
		return nil, err
	}
	if err := host.waitReady(ready, 5*time.Second); err != nil {
		return nil, fmt.Errorf("another Agent host is already running or local control could not start: %w",
			errors.Join(err, host.stop(time.Second)))
	}
	return host, nil
}

func (controller *guiController) buttons() fyne.CanvasObject {
	if runtime.GOOS == "windows" {
		stop := controller.actionButton("停止服务", theme.MediaStopIcon(), func() { controller.serviceAction("stop") })
		remove := controller.actionButton("卸载服务", theme.DeleteIcon(), func() { controller.serviceAction("uninstall") })
		remove.Importance = widget.DangerImportance
		return container.NewVBox(
			guiLabel("Windows 服务独立运行；退出此窗口不会停止服务。", false),
			container.NewGridWithColumns(2,
				controller.actionButton("安装服务", theme.DownloadIcon(), func() { controller.serviceAction("install") }),
				controller.actionButton("启动服务", theme.MediaPlayIcon(), func() { controller.serviceAction("start") }), stop, remove,
			),
			controller.actionButton("Modem 模式", theme.SettingsIcon(), controller.showRawModeDialog),
		)
	}
	return container.NewVBox(
		guiLabel("关闭窗口仅隐藏；退出 MDD Agent 会停止本进程。硬件启停不重新加载配置。", false),
		container.NewGridWithColumns(2,
			controller.actionButton("启动硬件", theme.MediaPlayIcon(), func() { controller.runtimeAction("start") }),
			controller.actionButton("停止硬件", theme.MediaStopIcon(), func() { controller.runtimeAction("stop") }),
		),
	)
}

func (controller *guiController) requestRefresh() {
	select {
	case controller.refresh <- struct{}{}:
	default:
	}
}

func (controller *guiController) refreshLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-controller.ctx.Done():
			return
		case <-ticker.C:
			controller.loadSnapshot()
		case <-controller.refresh:
			controller.loadSnapshot()
		}
	}
}

func (controller *guiController) loadSnapshot() {
	value := map[string]any{}
	if saved, err := readConnectionEdit(controller.configPath); err != nil {
		value["saved_config_error"] = true
	} else {
		value["saved_config"] = guiSavedConnection(saved.settings, controller.settings)
	}
	if runtime.GOOS == "windows" {
		var output bytes.Buffer
		if err := controller.runWindowsService("service-status", &output); err != nil {
			value["service_error"] = err.Error()
		} else {
			var status any
			if err := json.Unmarshal(output.Bytes(), &status); err != nil {
				value["service_error"] = err.Error()
			} else {
				value["service"] = status
			}
		}
	}
	var output bytes.Buffer
	if err := runClient("status", controller.settings, &output); err != nil {
		value["runtime_error"] = err.Error()
	} else {
		var snapshot agentcontrol.Snapshot
		if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
			value["runtime_error"] = err.Error()
		} else {
			value["runtime"] = snapshot
		}
	}
	if runtime.GOOS == "windows" {
		output.Reset()
		if err := runRawModeClient("raw-modes", nil, controller.settings, &output); err != nil {
			value["raw_modes_error"] = err.Error()
		} else {
			var modes any
			if err := json.Unmarshal(output.Bytes(), &modes); err != nil {
				value["raw_modes_error"] = err.Error()
			} else {
				value["raw_modes"] = modes
			}
		}
	}
	output.Reset()
	if err := runClient("topology", controller.settings, &output); err != nil {
		value["topology_error"] = err.Error()
	} else {
		var topology agentlink.TopologySnapshot
		if err := json.Unmarshal(output.Bytes(), &topology); err != nil {
			value["topology_error"] = err.Error()
		} else {
			value["topology"] = topology
		}
	}
	payload, _ := json.MarshalIndent(value, "", "  ")
	summary := guiSummary(value)
	if controller.ctx.Err() != nil {
		return
	}
	if rendered := summary + "\x00" + string(payload); rendered == controller.lastRender {
		return
	} else {
		controller.lastRender = rendered
	}
	fyne.Do(func() {
		if controller.ctx.Err() != nil {
			return
		}
		controller.observed = nil
		if snapshot, ok := value["runtime"].(agentcontrol.Snapshot); ok {
			controller.observed = &snapshot
		}
		controller.renderSnapshot(value)
	})
}

func (controller *guiController) showRawModeDialog() {
	if !controller.reserveAction() {
		return
	}
	equipment := widget.NewEntry()
	equipment.SetPlaceHolder("IMEI / equipment ID")
	iccid := widget.NewEntry()
	iccid.SetPlaceHolder("ICCID")
	mode := widget.NewSelect([]string{"raw", "adapted"}, nil)
	mode.SetSelected("raw")
	dialog.ShowForm("持久 Modem 模式", "应用", "取消", []*widget.FormItem{
		widget.NewFormItem("Equipment ID", equipment),
		widget.NewFormItem("ICCID", iccid),
		widget.NewFormItem("模式", mode),
	}, func(confirmed bool) {
		if !confirmed {
			controller.releaseAction()
			return
		}
		equipmentID, cardID, selectedMode := equipment.Text, iccid.Text, mode.Selected
		controller.background(func() error {
			if err := checkGUIServiceTarget(controller.configPath, false); err != nil {
				return err
			}
			return runRawModeClient("raw-mode", []string{equipmentID, cardID, selectedMode},
				controller.settings, &bytes.Buffer{})
		})
	}, controller.window)
}

func guiSummary(value map[string]any) string {
	serviceState := ""
	if serviceValue, ok := value["service"].(map[string]any); ok {
		serviceState, _ = serviceValue["state"].(string)
	}
	runtimeState := "unavailable"
	if snapshot, ok := value["runtime"].(agentcontrol.Snapshot); ok {
		runtimeState = string(snapshot.State)
	}
	readerState := "unavailable"
	modemState := "unavailable"
	if topology, ok := value["topology"].(agentlink.TopologySnapshot); ok {
		readerState = string(topology.ReaderCondition)
		modemState = string(topology.ModemCondition)
	}
	if serviceState != "" {
		return fmt.Sprintf("服务：%s    运行时：%s    PC/SC：%s    Modem：%s", serviceState, runtimeState, readerState, modemState)
	}
	return fmt.Sprintf("运行时：%s    PC/SC：%s    Modem：%s", runtimeState, readerState, modemState)
}

func (controller *guiController) serviceAction(action string) {
	label := map[string]string{"install": "安装服务", "start": "启动服务", "stop": "停止服务", "uninstall": "卸载服务"}[action]
	if label == "" {
		return
	}
	controller.confirmAction("确认服务操作", label+"\n配置："+controller.configPath+"\n停止服务会中断此 Agent 的业务。卸载仅移除已停止的服务，不删除配置或隔离保护。", func() error {
		if err := checkGUIServiceTarget(controller.configPath, action == "install"); err != nil {
			return err
		}
		var output bytes.Buffer
		return controller.runWindowsService("service-"+action, &output)
	})
}

func (controller *guiController) runWindowsService(command string, output *bytes.Buffer) error {
	current, err := os.Executable()
	if err != nil {
		return err
	}
	serviceExecutable := filepath.Join(filepath.Dir(current), "mdd-agent.exe")
	info, err := os.Stat(serviceExecutable)
	if err != nil {
		return fmt.Errorf("locate the packaged service/CLI executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("packaged service/CLI executable is not a regular file")
	}
	return runOSServiceWithExecutable(command, controller.configPath, serviceExecutable, controller.settings, output)
}

func (controller *guiController) runtimeAction(action string) {
	label := map[string]string{"start": "启动硬件", "stop": "停止硬件"}[action]
	if label == "" {
		return
	}
	if controller.observed == nil {
		dialog.ShowError(errors.New("请先读取本地运行状态"), controller.window)
		return
	}
	observed := *controller.observed
	controller.confirmAction("确认硬件操作", label+"\n此操作只影响当前宿主；停止会中断其读卡器和 Modem 业务，不会重新加载磁盘配置。", func() error {
		var output bytes.Buffer
		if err := runClient("status", controller.settings, &output); err != nil {
			return err
		}
		var current agentcontrol.Snapshot
		if err := json.Unmarshal(output.Bytes(), &current); err != nil {
			return err
		}
		if current.Generation != observed.Generation || current.State != observed.State {
			return errors.New("Agent 状态已变化，请刷新后重新确认")
		}
		return runClient(action, controller.settings, &bytes.Buffer{})
	})
}

func (controller *guiController) background(operation func() error) {
	go func() {
		if controller.ctx.Err() != nil {
			return
		}
		err := operation()
		fyne.Do(func() {
			if controller.ctx.Err() != nil {
				return
			}
			controller.releaseAction()
			if err != nil {
				controller.actionStatus.SetText("操作未确认完成；未自动重试")
				dialog.ShowError(err, controller.window)
			} else {
				controller.actionStatus.SetText("操作已返回；正在读取当前状态")
			}
		})
		controller.requestRefresh()
	}()
}

func (controller *guiController) actionButton(label string, icon fyne.Resource, action func()) *widget.Button {
	button := widget.NewButtonWithIcon(label, icon, action)
	controller.actionButtons = append(controller.actionButtons, button)
	return button
}

func (controller *guiController) reserveAction() bool {
	if controller.actionBusy || controller.ctx.Err() != nil {
		return false
	}
	controller.actionBusy = true
	for _, button := range controller.actionButtons {
		button.Disable()
	}
	return true
}

func (controller *guiController) releaseAction() {
	controller.actionBusy = false
	for _, button := range controller.actionButtons {
		button.Enable()
	}
}

func (controller *guiController) confirmAction(title, message string, operation func() error) {
	if !controller.reserveAction() {
		return
	}
	dialog.ShowConfirm(title, message, func(confirmed bool) {
		if !confirmed || controller.ctx.Err() != nil {
			controller.releaseAction()
			return
		}
		controller.actionStatus.SetText("操作进行中")
		controller.background(operation)
	}, controller.window)
}
