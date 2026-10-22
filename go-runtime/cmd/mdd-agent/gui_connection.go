//go:build gui && (darwin || windows)

package main

import (
	"context"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (controller *guiController) editConnection() {
	if !controller.reserveAction() {
		return
	}
	go func() {
		edit, err := readConnectionEdit(controller.configPath)
		fyne.Do(func() {
			if controller.ctx.Err() != nil {
				return
			}
			if err != nil {
				controller.releaseAction()
				dialog.ShowError(err, controller.window)
				return
			}
			controller.connectionDialog(edit)
		})
	}()
}

func (controller *guiController) connectionDialog(edit connectionEdit) {
	ctx, cancel := context.WithCancel(controller.ctx)
	server := widget.NewEntry()
	server.SetText(edit.settings.Agent.ServerURL)
	pin := widget.NewEntry()
	pin.SetText(edit.settings.Agent.TLSFingerprint)
	token := widget.NewPasswordEntry()
	token.SetPlaceHolder("已保存；留空保持原值")
	trust := widget.NewSelect([]string{"系统证书验证", "固定证书指纹"}, nil)
	trust.SetSelected("系统证书验证")
	if edit.settings.Agent.TLSMode == "pin" || edit.settings.Agent.TLSFingerprint != "" {
		trust.SetSelected("固定证书指纹")
	}
	form := widget.NewForm()
	fields := func() {
		form.Items = []*widget.FormItem{widget.NewFormItem("Core 地址", server), widget.NewFormItem("证书信任", trust)}
		if trust.Selected == "固定证书指纹" {
			form.Append("可信 SHA-256", pin)
		}
		form.Append("替换 Agent token", token)
		form.Refresh()
	}
	trust.OnChanged = func(string) { fields() }
	fields()
	status := guiLabel("仅修改已保存的连接配置；原 Agent ID、控制密钥和设备策略保持不变。", false)
	identity := guiLabel("Agent ID："+edit.settings.Agent.ID+"\n配置："+edit.path, false)
	closed, inFlight := false, false
	var save *widget.Button
	save = widget.NewButtonWithIcon("验证并保存", theme.DocumentSaveIcon(), func() {
		if inFlight || closed {
			return
		}
		change := connectionChange{Server: server.Text, TLSMode: "system", Token: token.Text}
		if trust.Selected == "固定证书指纹" {
			change.TLSMode, change.Fingerprint = "pin", pin.Text
		}
		inFlight = true
		save.Disable()
		form.Disable()
		dialog.ShowConfirm("确认连接目标", "服务端："+change.Server+"\n证书信任："+trust.Selected+"\n验证将向此目标发送当前 Agent 的凭据。保存不会重启或替换正在运行的 Agent。", func(confirmed bool) {
			if !confirmed || closed || ctx.Err() != nil {
				inFlight = false
				if closed {
					controller.releaseAction()
				} else {
					save.Enable()
					form.Enable()
				}
				return
			}
			token.SetText("")
			status.SetText("正在验证；尚未保存")
			go func() {
				op, finish := context.WithTimeout(ctx, 45*time.Second)
				err := edit.save(op, change)
				finish()
				change.Token = ""
				var saved connectionEdit
				var readErr error
				if err == nil {
					saved, readErr = readConnectionEdit(edit.path)
				}
				fyne.Do(func() {
					if controller.ctx.Err() != nil {
						return
					}
					inFlight = false
					if err == nil {
						if readErr == nil {
							controller.showSavedConfig(saved.settings)
						} else {
							controller.savedInfo.SetText("配置已保存，但重新读取失败；请刷新后核对。")
						}
						controller.savedStatus.SetText("已保存；需由原 Agent 宿主重新加载后生效。当前运行进程未改变。")
						controller.actionStatus.SetText("连接配置已保存；未自动重启")
						if !closed {
							status.SetText("已保存。macOS 需完整退出并重新启动原宿主；Windows 需由原服务重新启动后加载。")
						}
					} else if !closed {
						status.SetText(err.Error())
						status.Importance = widget.DangerImportance
						status.Refresh()
						save.Enable()
						form.Enable()
					}
					if closed {
						controller.releaseAction()
					}
				})
			}()
		}, controller.window)
	})
	save.Importance = widget.HighImportance
	content := container.NewVBox(identity, widget.NewSeparator(), form, status, container.NewHBox(save))
	popup := dialog.NewCustom("修改已保存配置", "关闭", content, controller.window)
	popup.SetOnClosed(func() {
		closed = true
		cancel()
		if !inFlight {
			controller.releaseAction()
		}
	})
	popup.Resize(fyne.NewSize(680, 440))
	popup.Show()
}
