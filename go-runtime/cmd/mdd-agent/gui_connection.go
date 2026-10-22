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
		if controller.serviceClient != nil {
			state, err := controller.serviceClient.request(controller.ctx, nil)
			fyne.Do(func() {
				if controller.ctx.Err() != nil {
					return
				}
				if err != nil {
					controller.releaseAction()
					dialog.ShowError(err, controller.window)
					return
				}
				controller.connectionDialogManaged(connectionEdit{path: controller.configPath, settings: state.Saved.guiConfig()}, &state)
			})
			return
		}
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
	controller.connectionDialogManaged(edit, nil)
}

func (controller *guiController) connectionDialogManaged(edit connectionEdit, managed *localManagementState) {
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
	setInputsEnabled := func(enabled bool) {
		for _, input := range []fyne.Disableable{server, trust, pin, token} {
			if enabled {
				input.Enable()
			} else {
				input.Disable()
			}
		}
	}
	status := guiLabel("仅修改已保存的连接配置；原 Agent ID、控制密钥和设备策略保持不变。", false)
	if managed != nil {
		status.SetText("更换目标或证书信任需提供新目标 token；原凭据不会带到新目标。保存不会重启服务。")
	}
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
		setInputsEnabled(false)
		credentialNotice := "验证将向此目标发送当前 Agent 的凭据。"
		if managed != nil {
			credentialNotice = "验证只发送你填写的替换凭据，或在原目标与原信任不变时使用已保存凭据。"
		}
		dialog.ShowConfirm("确认连接目标", "服务端："+change.Server+"\n证书信任："+trust.Selected+"\n"+credentialNotice+"保存不会重启或替换正在运行的 Agent。", func(confirmed bool) {
			if !confirmed || closed || ctx.Err() != nil {
				inFlight = false
				if closed {
					controller.releaseAction()
				} else {
					save.Enable()
					setInputsEnabled(true)
				}
				return
			}
			token.SetText("")
			status.SetText("正在验证；尚未保存")
			go func() {
				op, finish := context.WithTimeout(ctx, 45*time.Second)
				var err error
				var result localManagementState
				if managed == nil {
					err = edit.save(op, change)
				} else {
					result, err = controller.serviceClient.request(op, &localManagementEdit{Version: localManagementVersion, Session: managed.Session, Revision: managed.Revision, Change: change})
				}
				finish()
				change.Token = ""
				var saved connectionEdit
				var readErr error
				if err == nil {
					if managed == nil {
						saved, readErr = readConnectionEdit(edit.path)
					} else {
						saved.settings = result.Saved.guiConfig()
					}
				}
				fyne.Do(func() {
					if controller.ctx.Err() != nil || controller.configPath != edit.path {
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
							if managed == nil {
								status.SetText("已保存。macOS 需完整退出并重新启动原宿主；Windows 需由原服务重新启动后加载。")
							} else {
								status.SetText("已保存，尚未加载。请打开当前网关，在 Agent 主机页受控重启后读回；离线或迁移至其他 Core 需管理员维护。重启不会绑定本次配置修订。")
							}
						}
					} else if !closed {
						status.SetText(err.Error())
						status.Importance = widget.DangerImportance
						status.Refresh()
						if managed == nil {
							save.Enable()
							setInputsEnabled(true)
						} else {
							status.SetText(err.Error() + "\n请关闭编辑器并刷新，读取当前修订后再操作。")
						}
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
