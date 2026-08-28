//go:build gui && (darwin || windows)

package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

//go:embed assets/mdd-agent.svg
var guiIcon []byte

type guiController struct {
	settings   config
	configPath string
	window     fyne.Window
	summary    *widget.Label
	details    *widget.Entry
	ctx        context.Context
	refresh    chan struct{}
	lastRender string
}

func runGUI(settings config, configPath string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var host *managedHost
	if runtime.GOOS == "darwin" {
		var err error
		host, err = startGUIHost(settings)
		if err != nil {
			return err
		}
	}

	application := app.NewWithID("com.mdd.agent")
	icon := fyne.NewStaticResource("mdd-agent.svg", guiIcon)
	application.SetIcon(icon)
	window := application.NewWindow("MDD Agent")
	window.SetIcon(icon)
	window.Resize(fyne.NewSize(760, 520))
	summary := widget.NewLabel("正在读取 Agent 状态…")
	summary.TextStyle = fyne.TextStyle{Bold: true}
	details := widget.NewMultiLineEntry()
	details.Disable()
	details.Wrapping = fyne.TextWrapOff
	controller := &guiController{
		settings: settings, configPath: configPath, window: window,
		summary: summary, details: details, ctx: ctx, refresh: make(chan struct{}, 1),
	}

	buttons := controller.buttons()
	window.SetContent(container.NewBorder(
		container.NewVBox(summary, buttons), nil, nil, nil, details,
	))
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
	go controller.refreshLoop()
	controller.requestRefresh()
	window.ShowAndRun()
	cancel()
	if host != nil {
		return host.stop(serviceStopTimeout(settings))
	}
	return nil
}

func startGUIHost(settings config) (*managedHost, error) {
	worker, err := buildWorker(settings)
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
	refresh := widget.NewButton("刷新", controller.requestRefresh)
	if runtime.GOOS == "windows" {
		return container.NewHBox(
			widget.NewButton("安装服务", func() { controller.serviceAction("install") }),
			widget.NewButton("启动服务", func() { controller.serviceAction("start") }),
			widget.NewButton("停止服务", func() { controller.serviceAction("stop") }),
			widget.NewButton("卸载服务", func() { controller.serviceAction("uninstall") }),
			refresh,
		)
	}
	return container.NewHBox(
		widget.NewButton("启动硬件", func() { controller.runtimeAction("start") }),
		widget.NewButton("停止硬件", func() { controller.runtimeAction("stop") }),
		refresh,
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
	if runtime.GOOS == "windows" {
		var output bytes.Buffer
		if err := runOSService("service-status", controller.configPath, controller.settings, &output); err != nil {
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
		controller.summary.SetText(summary)
		controller.details.SetText(string(payload))
	})
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
	if topology, ok := value["topology"].(agentlink.TopologySnapshot); ok {
		readerState = string(topology.ReaderCondition)
	}
	if serviceState != "" {
		return fmt.Sprintf("服务：%s    运行时：%s    PC/SC：%s", serviceState, runtimeState, readerState)
	}
	return fmt.Sprintf("运行时：%s    PC/SC：%s", runtimeState, readerState)
}

func (controller *guiController) serviceAction(action string) {
	controller.background(func() error {
		var output bytes.Buffer
		return runOSService("service-"+action, controller.configPath, controller.settings, &output)
	})
}

func (controller *guiController) runtimeAction(action string) {
	controller.background(func() error {
		return runClient(action, controller.settings, &bytes.Buffer{})
	})
}

func (controller *guiController) background(operation func() error) {
	go func() {
		if err := operation(); err != nil {
			if controller.ctx.Err() == nil {
				fyne.Do(func() { dialog.ShowError(err, controller.window) })
			}
			return
		}
		controller.requestRefresh()
	}()
}
