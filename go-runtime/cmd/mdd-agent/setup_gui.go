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
	"fyne.io/fyne/v2/theme"
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
	for _, candidate := range paths {
		known := false
		for _, existing := range found {
			known = known || existing.Path == candidate
		}
		if pending, err := readPendingSetup(candidate); !known && !errors.Is(err, os.ErrNotExist) {
			found = append(found, configCandidate{Path: candidate, AgentID: pending.Settings.Agent.ID, Err: err})
		}
	}
	var existingError error
	if len(found) == 1 {
		if found[0].Err != nil {
			existingError = found[0].Err
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
	message := widget.NewLabel("连接 MDD 网关")
	message.TextStyle.Bold = true
	message.Wrapping = fyne.TextWrapWord
	if len(found) > 1 {
		message.SetText("发现多个配置，请选择原配置。不会迁移配置或状态目录。")
	}
	if existingError != nil {
		message.SetText("已有配置不可读取：" + existingError.Error())
		message.Importance = widget.DangerImportance
	}
	server := widget.NewEntry()
	server.SetPlaceHolder("https://gateway.example:8443")
	username := widget.NewEntry()
	username.SetText("admin")
	password := widget.NewPasswordEntry()
	pin := widget.NewEntry()
	pin.SetPlaceHolder("可信网页提供的 SHA-256；不使用网络中首次看到的指纹")
	trust := widget.NewSelect([]string{"系统证书验证", "固定证书指纹"}, nil)
	trust.SetSelected("系统证书验证")
	bundle := widget.NewPasswordEntry()
	bundle.SetPlaceHolder("网页“添加 Agent”复制的接入 JSON")
	agentID := widget.NewEntry()
	token := widget.NewPasswordEntry()
	mode := widget.NewSelect([]string{"网页登录", "接入包", "已有 Agent 凭据"}, nil)
	mode.SetSelected("网页登录")
	commonItems := []*widget.FormItem{
		widget.NewFormItem("接入方式", mode),
	}
	serverItem := widget.NewFormItem("Core 地址", server)
	pinItem := widget.NewFormItem("可信证书指纹", pin)
	trustItem := widget.NewFormItem("证书信任", trust)
	form := widget.NewForm()
	updateFields := func() {
		form.Items = append([]*widget.FormItem(nil), commonItems...)
		switch mode.Selected {
		case "网页登录":
			form.Items = append(form.Items, serverItem, widget.NewFormItem("网页用户名", username), widget.NewFormItem("网页密码（不保存）", password), trustItem)
			if trust.Selected == "固定证书指纹" {
				form.Items = append(form.Items, pinItem)
			}
		case "接入包":
			form.Items = append(form.Items, widget.NewFormItem("接入 JSON", bundle))
		case "已有 Agent 凭据":
			form.Items = append(form.Items, serverItem, widget.NewFormItem("Agent ID", agentID), widget.NewFormItem("Agent token", token), trustItem)
			if trust.Selected == "固定证书指纹" {
				form.Items = append(form.Items, pinItem)
			}
		}
		form.Refresh()
	}
	mode.OnChanged = func(string) { updateFields() }
	trust.OnChanged = func(string) { updateFields() }
	updateFields()
	if settings, err := readConfigForEdit(pathEntry.Text, false); err == nil {
		server.SetText(settings.Agent.ServerURL)
		pin.SetText(settings.Agent.TLSFingerprint)
		if settings.Agent.TLSMode == "pin" || settings.Agent.TLSFingerprint != "" {
			trust.SetSelected("固定证书指纹")
		}
		agentID.SetText(settings.Agent.ID)
		message.SetText("继续未完成配置；保留原 Agent ID、控制密钥、用户开关和状态。")
	}
	resumeBound := false
	loadPending := func() {
		if pending, err := readPendingSetup(pathEntry.Text); err == nil {
			resumeBound = pending.Requested
			server.SetText(pending.Settings.Agent.ServerURL)
			pin.SetText(pending.Settings.Agent.TLSFingerprint)
			agentID.SetText(pending.Settings.Agent.ID)
			if pending.Settings.Agent.TLSMode == "pin" || pending.Settings.Agent.TLSFingerprint != "" {
				trust.SetSelected("固定证书指纹")
			} else {
				trust.SetSelected("系统证书验证")
			}
			if pending.Settings.Agent.ServerToken != "" {
				mode.SetSelected("已有 Agent 凭据")
				token.SetPlaceHolder("原接入凭据已保存；留空继续")
			}
			message.SetText("继续原接入：" + pending.Settings.Agent.ID + "；身份、服务端与证书信任沿用已保存回执。")
		} else if !errors.Is(err, os.ErrNotExist) {
			message.SetText("接入回执不可读取：" + err.Error())
			message.Importance = widget.DangerImportance
			existingError = err
		}
	}
	loadPending()
	busy := false
	locationForm := widget.NewForm(widget.NewFormItem("已发现路径", choice), widget.NewFormItem("配置文件（绝对路径）", pathEntry))
	location := widget.NewAccordion(widget.NewAccordionItem("配置位置", locationForm))
	statePresent := false
	if filepath.IsAbs(pathEntry.Text) {
		entries, stateErr := os.ReadDir(filepath.Join(filepath.Dir(pathEntry.Text), "state"))
		statePresent = len(entries) > 0 || stateErr != nil && !errors.Is(stateErr, os.ErrNotExist)
	}
	if len(found) > 1 || existingError != nil || locationErr != nil || statePresent || resumeBound {
		location.Open(0)
	}
	freezeResume := func() {
		if resumeBound {
			mode.Disable()
			server.Disable()
			pin.Disable()
			trust.Disable()
			agentID.Disable()
			token.Disable()
			locationForm.Disable()
		}
	}
	freezeResume()
	pathEntry.OnChanged = func(string) {
		loadPending()
		freezeResume()
	}
	var submit *widget.Button
	submit = widget.NewButtonWithIcon("验证并保存", theme.DocumentSaveIcon(), func() {
		if busy {
			return
		}
		selected := strings.TrimSpace(pathEntry.Text)
		if !filepath.IsAbs(selected) {
			dialog.ShowError(errors.New("请选择绝对配置路径"), window)
			location.Open(0)
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
		locationForm.Disable()
		input := setupInput{Server: server.Text, TLSMode: "system"}
		if mode.Selected != "接入包" && trust.Selected == "固定证书指纹" {
			if _, err := normalizeSHA256(pin.Text); err != nil {
				busy = false
				submit.Enable()
				form.Enable()
				locationForm.Enable()
				freezeResume()
				dialog.ShowError(err, window)
				return
			}
			input.Fingerprint = pin.Text
			input.TLSMode = "pin"
		}
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
				if ctx.Err() != nil {
					return
				}
				busy = false
				submit.Enable()
				if err != nil {
					form.Enable()
					locationForm.Enable()
					loadPending()
					freezeResume()
					location.Open(0)
					message.SetText("配置未完成；同路径重试会继续原接入。" + warning)
					if errors.Is(err, os.ErrPermission) {
						message.SetText("路径不可写，请选择可写的绝对路径。未自动提权。")
						location.Open(0)
					}
					dialog.ShowError(err, window)
					return
				}
				message.SetText(fmt.Sprintf("已保存 %s。TLS 与凭据已验证；实际连接待启动。%s", selected, warning))
				submit.SetText("进入 Agent")
				submit.OnTapped = func() { ready(settings, selected) }
				form.Disable()
				locationForm.Disable()
			})
		}()
	})
	submit.Importance = widget.HighImportance
	title := guiLabel("MDD Agent", true)
	window.SetContent(container.NewPadded(container.NewBorder(container.NewVBox(title, message, widget.NewSeparator()),
		container.NewPadded(submit), nil, nil, container.NewVScroll(container.NewVBox(form, location)))))
}
