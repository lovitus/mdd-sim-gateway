//go:build gui && (darwin || windows)

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func showGUISetup(ctx context.Context, window fyne.Window, requested string, ready func(config, string)) {
	path, err := explicitOrEnvironmentPath(requested)
	if err != nil {
		window.SetContent(widget.NewLabel(err.Error()))
		return
	}
	paths, locationErr := configLocations()
	if path != "" {
		paths = []string{path}
	} else if locationErr != nil {
		paths = nil
	}
	found := discoverConfigs(paths)
	if len(found) == 1 {
		if found[0].Err != nil {
			window.SetContent(widget.NewLabel(found[0].Path + ": " + found[0].Err.Error()))
			return
		}
		path = found[0].Path
		if _, pendingErr := os.Lstat(path + ".setup-pending"); errors.Is(pendingErr, os.ErrNotExist) {
			if settings, err := loadConfig(path); err == nil {
				ready(settings, path)
				return
			}
		}
	}
	pathEntry := widget.NewEntry()
	pathEntry.SetText(path)
	if path == "" && len(paths) > 0 {
		pathEntry.SetText(paths[0])
	}
	var items []string
	choicePaths := map[string]string{}
	for _, candidate := range paths {
		label := candidate
		for _, known := range found {
			if known.Path == candidate {
				if known.Err != nil {
					label += " (" + known.Err.Error() + ")"
				} else {
					label += " (" + known.AgentID + ")"
				}
			}
		}
		items = append(items, label)
		choicePaths[label] = candidate
	}
	choice := widget.NewSelect(items, func(value string) { pathEntry.SetText(choicePaths[value]) })
	message := widget.NewLabel("首次配置")
	message.TextStyle.Bold = true
	message.Wrapping = fyne.TextWrapWord
	if len(found) > 1 {
		message.SetText("发现多个配置，请选择原配置。不会迁移配置或状态目录。")
	}
	server := widget.NewEntry()
	server.SetPlaceHolder("https://gateway.example:8443")
	username := widget.NewEntry()
	username.SetText("admin")
	password := widget.NewPasswordEntry()
	pin := widget.NewEntry()
	pin.SetPlaceHolder("留空使用系统 CA；自签证书填写可信网页提供的 SHA-256")
	bundle := widget.NewPasswordEntry()
	bundle.SetPlaceHolder("网页“添加 Agent”复制的接入 JSON")
	agentID := widget.NewEntry()
	token := widget.NewPasswordEntry()
	mode := widget.NewSelect([]string{"网页登录", "接入包", "已有 Agent 凭据"}, nil)
	mode.SetSelected("网页登录")
	form := widget.NewForm(
		widget.NewFormItem("已发现路径", choice), widget.NewFormItem("配置文件（绝对路径）", pathEntry),
		widget.NewFormItem("接入方式", mode), widget.NewFormItem("Core 地址", server),
		widget.NewFormItem("可信证书指纹（可选）", pin), widget.NewFormItem("网页用户名", username),
		widget.NewFormItem("网页密码（不保存）", password), widget.NewFormItem("接入 JSON", bundle),
		widget.NewFormItem("Agent ID", agentID), widget.NewFormItem("Agent token", token),
	)
	updateFields := func() {
		for _, entry := range []*widget.Entry{server, pin, username, password, bundle, agentID, token} {
			entry.Hide()
		}
		switch mode.Selected {
		case "网页登录":
			server.Show()
			pin.Show()
			username.Show()
			password.Show()
		case "接入包":
			bundle.Show()
		case "已有 Agent 凭据":
			server.Show()
			pin.Show()
			agentID.Show()
			token.Show()
		}
		form.Refresh()
	}
	mode.OnChanged = func(string) { updateFields() }
	updateFields()
	if settings, err := readConfigForEdit(pathEntry.Text, false); err == nil {
		server.SetText(settings.Agent.ServerURL)
		pin.SetText(settings.Agent.TLSFingerprint)
		agentID.SetText(settings.Agent.ID)
		message.SetText("继续未完成配置；保留原 Agent ID、控制密钥、用户开关和状态。")
	}
	busy := false
	var submit *widget.Button
	submit = widget.NewButton("验证并保存", func() {
		if busy {
			return
		}
		selected := strings.TrimSpace(pathEntry.Text)
		if !filepath.IsAbs(selected) {
			dialog.ShowError(errors.New("请选择绝对配置路径"), window)
			return
		}
		if _, pendingErr := os.Lstat(selected + ".setup-pending"); errors.Is(pendingErr, os.ErrNotExist) {
			if settings, err := loadConfig(selected); err == nil {
				ready(settings, selected)
				return
			}
		}
		busy = true
		submit.Disable()
		form.Disable()
		input := setupInput{Server: server.Text, Fingerprint: pin.Text}
		switch mode.Selected {
		case "网页登录":
			input.Username, input.Password = username.Text, password.Text
		case "接入包":
			input = setupInput{Package: bundle.Text}
		case "已有 Agent 凭据":
			input.AgentID, input.Token = agentID.Text, token.Text
		}
		password.SetText("")
		token.SetText("")
		bundle.SetText("")
		message.SetText("正在验证并保存；尚未启动硬件")
		go func() {
			tx, err := beginSetup(selected)
			var settings config
			var warning string
			if err == nil {
				op, cancel := context.WithTimeout(ctx, 90*time.Second)
				settings, warning, err = tx.enroll(op, input)
				cancel()
				tx.close()
			}
			input.Password, input.Token, input.Package = "", "", ""
			if ctx.Err() != nil {
				return
			}
			fyne.Do(func() {
				busy = false
				submit.Enable()
				if err != nil {
					form.Enable()
					message.SetText("配置未完成；同路径重试会继续原接入。" + warning)
					if errors.Is(err, os.ErrPermission) {
						message.SetText("路径不可写，请选择可写的绝对路径。未自动提权。")
					}
					dialog.ShowError(err, window)
					return
				}
				message.SetText(fmt.Sprintf("已保存 %s。TLS 与凭据已验证；实际连接待启动。%s", selected, warning))
				submit.SetText("进入 Agent")
				submit.OnTapped = func() { ready(settings, selected) }
				form.Disable()
			})
		}()
	})
	window.SetContent(container.NewBorder(message, submit, nil, nil, container.NewVScroll(form)))
}
