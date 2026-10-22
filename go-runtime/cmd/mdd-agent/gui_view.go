//go:build gui && (darwin || windows)

package main

import (
	"encoding/json"
	"fmt"
	"image/color"
	"net/url"
	"runtime"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

type agentTheme struct{ fyne.Theme }

func (current agentTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	dark := variant == theme.VariantDark
	switch name {
	case theme.ColorNameForegroundOnPrimary, theme.ColorNameForegroundOnSuccess, theme.ColorNameForegroundOnWarning, theme.ColorNameForegroundOnError:
		if dark {
			return color.NRGBA{R: 18, G: 25, B: 28, A: 255}
		}
		return color.White
	case theme.ColorNamePrimary:
		if dark {
			return color.NRGBA{R: 79, G: 195, B: 183, A: 255}
		}
		return color.NRGBA{R: 0, G: 103, B: 94, A: 255}
	case theme.ColorNameSuccess:
		if dark {
			return color.NRGBA{R: 104, G: 211, B: 145, A: 255}
		}
		return color.NRGBA{R: 24, G: 111, B: 57, A: 255}
	case theme.ColorNameWarning:
		if dark {
			return color.NRGBA{R: 255, G: 200, B: 87, A: 255}
		}
		return color.NRGBA{R: 146, G: 84, B: 0, A: 255}
	case theme.ColorNameError:
		if dark {
			return color.NRGBA{R: 255, G: 139, B: 139, A: 255}
		}
		return color.NRGBA{R: 180, G: 35, B: 24, A: 255}
	}
	return current.Theme.Color(name, variant)
}

func guiLabel(text string, bold bool) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	label.TextStyle.Bold = bold
	return label
}

func guiStatus(text string, importance widget.Importance) *widget.Label {
	label := guiLabel(text, true)
	label.Importance = importance
	return label
}

func (controller *guiController) content() fyne.CanvasObject {
	controller.summary = guiLabel("正在读取本机运行状态", true)
	controller.connection = guiStatus("Core 连接：未确认", widget.WarningImportance)
	controller.service = guiStatus("Windows 服务：未确认", widget.WarningImportance)
	if runtime.GOOS != "windows" {
		controller.service.Hide()
	}
	controller.devices = container.NewVBox(guiLabel("正在读取设备", false))
	controller.actionStatus = guiLabel("", false)
	controller.savedStatus = guiLabel("磁盘配置；运行进程是否已加载此版本尚未确认", false)
	controller.savedInfo = guiLabel("", false)
	controller.showSavedConfig(controller.settings)
	refresh := widget.NewButtonWithIcon("刷新", theme.ViewRefreshIcon(), controller.requestRefresh)
	web := widget.NewButtonWithIcon("打开网关", theme.ComputerIcon(), func() {
		endpoint, err := url.Parse(controller.settings.Agent.ServerURL)
		if err != nil || endpoint.Scheme != "wss" || endpoint.User != nil {
			return
		}
		endpoint.Scheme, endpoint.Path, endpoint.RawQuery, endpoint.Fragment = "https", "/", "", ""
		if err = fyne.CurrentApp().OpenURL(endpoint); err != nil {
			controller.actionStatus.SetText(err.Error())
		}
	})
	header := container.NewBorder(nil, nil, guiLabel("MDD Agent", true), container.NewHBox(web, refresh), nil)
	overview := container.NewVScroll(container.NewVBox(
		guiLabel("本机运行状态", true), controller.service, controller.summary, controller.connection,
		widget.NewSeparator(), guiLabel("当前设备", true), controller.devices,
	))
	edit := controller.actionButton("修改连接配置", theme.SettingsIcon(), controller.editConnection)
	edit.Importance = widget.HighImportance
	settings := container.NewVScroll(container.NewVBox(
		guiLabel("已保存配置", true), controller.savedStatus, controller.savedInfo,
		container.NewHBox(edit), widget.NewSeparator(), guiLabel("运行管理", true), controller.buttons(),
	))
	controller.details = widget.NewMultiLineEntry()
	controller.details.Wrapping = fyne.TextWrapWord
	// Keep the diagnostic view selectable without rendering disabled low-contrast text.
	controller.details.OnChanged = func(value string) {
		if value != controller.diagnosticText {
			controller.details.SetText(controller.diagnosticText)
		}
	}
	copyDetails := widget.NewButtonWithIcon("复制脱敏诊断", theme.ContentCopyIcon(), func() {
		controller.window.Clipboard().SetContent(controller.diagnosticText)
	})
	diagnostics := container.NewBorder(container.NewHBox(copyDetails), nil, nil, nil, controller.details)
	tabs := container.NewAppTabs(
		container.NewTabItemWithIcon("概览", theme.ComputerIcon(), overview),
		container.NewTabItemWithIcon("配置", theme.SettingsIcon(), settings),
		container.NewTabItemWithIcon("诊断", theme.InfoIcon(), diagnostics),
	)
	return container.NewPadded(container.NewBorder(header, controller.actionStatus, nil, nil, tabs))
}

func (controller *guiController) showSavedConfig(settings config) {
	controller.savedInfo.SetText(guiSavedConnection(settings, controller.settings).Text + "\n配置文件：" + controller.configPath)
}

type guiSavedConfig struct {
	Text    string
	Changed bool
}

func guiSavedConnection(settings, launch config) guiSavedConfig {
	trust := "系统证书验证"
	if settings.Agent.TLSMode == "pin" || settings.Agent.TLSFingerprint != "" {
		trust = "固定证书指纹（已保存）"
	}
	return guiSavedConfig{
		Text: fmt.Sprintf("Agent ID：%s\nCore 地址：%s\n证书信任：%s\n凭据：已保存", settings.Agent.ID, settings.Agent.ServerURL, trust),
		Changed: settings.Agent.ID != launch.Agent.ID || settings.Agent.ServerURL != launch.Agent.ServerURL ||
			settings.Agent.TLSMode != launch.Agent.TLSMode || settings.Agent.TLSFingerprint != launch.Agent.TLSFingerprint ||
			settings.Agent.ServerToken != launch.Agent.ServerToken,
	}
}

func (controller *guiController) renderSnapshot(value map[string]any) {
	if runtime.GOOS == "windows" {
		controller.service.Importance = widget.WarningImportance
		controller.service.SetText("Windows 服务：状态不可读取")
		if service, ok := value["service"].(map[string]any); ok {
			state, _ := service["state"].(string)
			controller.service.SetText("Windows 服务：" + guiState(state))
			controller.service.Importance = guiStateImportance(state)
			if installed, known := service["installed"].(bool); known && !installed {
				controller.service.SetText("Windows 服务：未安装")
				controller.service.Importance = widget.MediumImportance
			}
		}
		controller.service.Refresh()
	}
	if saved, ok := value["saved_config"].(guiSavedConfig); ok {
		controller.savedInfo.SetText(saved.Text + "\n配置文件：" + controller.configPath)
		controller.savedStatus.SetText("磁盘配置；运行进程是否已加载此版本尚未确认")
		if saved.Changed {
			controller.savedStatus.SetText("磁盘连接配置已变化；当前 GUI 仍使用启动时的连接，原宿主是否已重新加载尚未确认")
		}
	} else if _, failed := value["saved_config_error"]; failed {
		controller.savedInfo.SetText("已保存配置不可读取；请核对配置文件。")
		controller.savedStatus.SetText("未确认磁盘配置；没有更改运行进程")
	}
	local, localImportance := "本地 Agent：状态不可读取", widget.WarningImportance
	connection, importance := "Core 连接：未确认", widget.WarningImportance
	if snapshot, ok := value["runtime"].(agentcontrol.Snapshot); ok {
		switch snapshot.State {
		case agentcontrol.StateRunning:
			local, localImportance = "本地 Agent：运行中", widget.SuccessImportance
		case agentcontrol.StateStopped:
			local, localImportance = "本地 Agent：已停止", widget.MediumImportance
		case agentcontrol.StateStarting:
			local = "本地 Agent：启动中"
		case agentcontrol.StateStopping:
			local = "本地 Agent：停止中"
		case agentcontrol.StateFailed:
			local, localImportance = "本地 Agent：运行失败", widget.DangerImportance
			if snapshot.Code != "" {
				local += "（" + snapshot.Code + "）"
			}
		}
		if snapshot.State != agentcontrol.StateRunning && snapshot.State != agentcontrol.StateStarting {
			connection, importance = "Core 连接：本地运行时未运行", widget.MediumImportance
		} else if link := snapshot.CoreConnection; link != nil {
			switch link.State {
			case "connected":
				connection, importance = "Core 连接：已连接", widget.SuccessImportance
			case "connecting":
				connection = "Core 连接：连接中"
			case "retrying":
				connection = "Core 连接：已断开，正在自动重连"
				if !link.RetryAt.IsZero() {
					connection += "；下次尝试 " + link.RetryAt.Local().Format("15:04:05")
				}
			case "disconnected", "stopped":
				connection, importance = "Core 连接：已断开", widget.DangerImportance
			}
		}
	}
	controller.summary.Importance = localImportance
	controller.summary.SetText(local)
	controller.connection.Importance = importance
	controller.connection.SetText(connection)
	controller.summary.Refresh()
	controller.connection.Refresh()
	rows := []fyne.CanvasObject{}
	if topology, ok := value["topology"].(agentlink.TopologySnapshot); ok {
		if topology.Host != nil {
			rows = append(rows, guiLabel(fmt.Sprintf("%s · %s %s · %s", topology.Host.Hostname, topology.Host.Platform,
				topology.Host.Architecture, topology.Host.BuildVersion), false))
		}
		rows = append(rows, guiLabel(fmt.Sprintf("读卡器：%d", len(topology.Readers)), true))
		for _, reader := range topology.Readers {
			state, level := "身份读取中", widget.WarningImportance
			switch {
			case !reader.CardPresent:
				state, level = "未插卡", widget.MediumImportance
			case reader.IdentityState == agentlink.CardIdentified && topology.ReaderCondition == agentlink.ReaderReady:
				state, level = "卡片已识别", widget.SuccessImportance
			case reader.IdentityState == agentlink.CardIdentityUnavailable:
				state, level = "卡片身份不可读取", widget.DangerImportance
			}
			rows = append(rows, container.NewVBox(guiLabel(reader.ReaderName, true), guiStatus(state, level),
				guiLabel("ICCID："+guiPresent(reader.CardID), false), widget.NewSeparator()))
		}
		if len(topology.Readers) == 0 {
			text, level := "未发现读卡器", widget.MediumImportance
			if topology.ReaderCondition != agentlink.ReaderReady {
				text, level = "读卡器状态："+guiState(string(topology.ReaderCondition)), widget.WarningImportance
			}
			rows = append(rows, guiStatus(text, level))
		}
		modemHeading := fmt.Sprintf("Modem：%d", len(topology.Modems))
		if topology.ModemCondition == agentlink.ModemDisabled {
			modemHeading = "Modem：已停用"
		}
		rows = append(rows, guiLabel(modemHeading, true))
		if len(topology.Modems) == 0 && topology.ModemCondition != agentlink.ModemDisabled {
			rows = append(rows, guiStatus("Modem 状态："+guiState(string(topology.ModemCondition)), guiStateImportance(string(topology.ModemCondition))))
		}
		for _, modem := range topology.Modems {
			name := strings.TrimSpace(modem.Manufacturer + " " + modem.Model)
			if name == "" {
				name = "Modem"
			}
			rows = append(rows, container.NewVBox(guiLabel(name, true),
				guiLabel("设备："+modem.EquipmentID+"\nICCID："+guiPresent(modem.SIM.ICCID)+"\n号码："+guiPresent(strings.Join(modem.SIM.MSISDNs, " / ")), false),
				guiStatus("数据："+guiState(modem.Network.Data), guiStateImportance(modem.Network.Data)),
				guiStatus("隔离："+guiState(modem.Network.DataGuard), guiStateImportance(modem.Network.DataGuard)),
				guiStatus("AT："+guiState(modem.AT.State), guiStateImportance(modem.AT.State)), widget.NewSeparator()))
		}
		if len(topology.RawUSBSessions)+len(topology.RawUSBRecoveries) > 0 {
			rows = append(rows, guiLabel("USB 穿透", true))
			for _, session := range topology.RawUSBSessions {
				rows = append(rows, guiLabel("设备："+session.EquipmentID+" · ICCID："+session.CardID, false),
					guiStatus(string(session.Role)+" · "+guiState(session.State), guiStateImportance(session.State)))
			}
			for _, recovery := range topology.RawUSBRecoveries {
				rows = append(rows, guiLabel("设备："+recovery.EquipmentID+" · ICCID："+recovery.CardID, false),
					guiStatus("保留占用："+guiState(recovery.State), widget.WarningImportance))
			}
		}
	} else {
		rows = append(rows, guiStatus("设备状态不可读取；没有当前设备健康证据", widget.WarningImportance))
	}
	controller.devices.Objects = rows
	controller.devices.Refresh()
	controller.diagnosticText = guiDiagnostic(value)
	controller.details.SetText(controller.diagnosticText)
}

func guiPresent(value string) string {
	if value == "" {
		return "未上报"
	}
	return value
}

func guiState(value string) string {
	switch value {
	case "ready":
		return "就绪"
	case "running":
		return "运行中"
	case "stopped":
		return "已停止"
	case "connected":
		return "已连接"
	case "protected":
		return "受保护"
	case "disabled", "off":
		return "已关闭"
	case "disconnected":
		return "未连接"
	case "connecting", "starting":
		return "连接中"
	case "recovering":
		return "恢复中"
	case "failed":
		return "失败"
	case "unavailable":
		return "不可用"
	case "unmanaged":
		return "未受隔离保护"
	case "", "unknown":
		return "未确认"
	default:
		return value
	}
}

func guiStateImportance(value string) widget.Importance {
	switch value {
	case "ready", "connected", "protected", "running":
		return widget.SuccessImportance
	case "off", "disabled", "disconnected", "stopped":
		return widget.MediumImportance
	case "failed", "unavailable":
		return widget.DangerImportance
	default:
		return widget.WarningImportance
	}
}

// Export an allowlist, not raw topology or transport errors. Local device rows
// remain useful while copied diagnostics omit identities, addresses and secrets.
func guiDiagnostic(value map[string]any) string {
	result := map[string]any{"platform": runtime.GOOS}
	if snapshot, ok := value["runtime"].(agentcontrol.Snapshot); ok {
		result["runtime_state"], result["changed_at"] = snapshot.State, snapshot.ChangedAt
		result["runtime_code"] = snapshot.Code
		result["core_connection"] = snapshot.CoreConnection
	} else {
		result["runtime_state"] = "unavailable"
	}
	if topology, ok := value["topology"].(agentlink.TopologySnapshot); ok {
		result["reader_condition"], result["reader_count"] = topology.ReaderCondition, len(topology.Readers)
		result["modem_condition"], result["modem_count"] = topology.ModemCondition, len(topology.Modems)
		result["raw_session_count"], result["raw_recovery_count"] = len(topology.RawUSBSessions), len(topology.RawUSBRecoveries)
		if topology.Host != nil {
			result["build_version"], result["architecture"] = topology.Host.BuildVersion, topology.Host.Architecture
		}
		modems := make([]map[string]string, 0, len(topology.Modems))
		for _, modem := range topology.Modems {
			modems = append(modems, map[string]string{"condition": modem.Condition, "at": modem.AT.State, "data": modem.Network.Data, "guard": modem.Network.DataGuard})
		}
		result["modems"] = modems
	} else {
		result["topology_state"] = "unavailable"
	}
	for _, key := range []string{"runtime_error", "service_error", "topology_error", "raw_modes_error"} {
		if _, ok := value[key]; ok {
			result[key] = "read_failed"
		}
	}
	payload, _ := json.MarshalIndent(result, "", "  ")
	return string(payload)
}
