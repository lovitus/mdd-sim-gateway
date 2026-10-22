//go:build gui && (darwin || windows)

package main

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestGUISetupModeShowsOnlyApplicableRows(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()
	window := application.NewWindow("Agent setup")
	defer window.Close()
	showGUISetup(context.Background(), window, filepath.Join(t.TempDir(), "config.json"), func(config, string) {
		t.Fatal("mode selection must not start the Agent")
	})
	var form *widget.Form
	for _, candidate := range test.LaidOutObjects(window.Content()) {
		if current, ok := candidate.(*widget.Form); ok {
			for _, item := range current.Items {
				if item.Text == "接入方式" {
					form = current
				}
			}
		}
	}
	if form == nil {
		t.Fatal("setup form is absent")
	}
	var mode *widget.Select
	for _, item := range form.Items {
		if item.Text == "接入方式" {
			mode, _ = item.Widget.(*widget.Select)
		}
	}
	if mode == nil {
		t.Fatal("setup mode selector is absent")
	}
	common := []string{"接入方式"}
	for _, step := range []struct {
		mode   string
		fields []string
	}{
		{"网页登录", []string{"Core 地址", "网页用户名", "网页密码（不保存）", "证书信任"}},
		{"接入包", []string{"接入 JSON"}},
		{"已有 Agent 凭据", []string{"Core 地址", "Agent ID", "Agent token", "证书信任"}},
		{"网页登录", []string{"Core 地址", "网页用户名", "网页密码（不保存）", "证书信任"}},
	} {
		mode.SetSelected(step.mode)
		var labels []string
		for _, item := range form.Items {
			labels = append(labels, item.Text)
			if !item.Widget.Visible() {
				t.Errorf("ORPHAN_SETUP_ROW: %s leaves label %q without its input", step.mode, item.Text)
			}
		}
		want := append(append([]string(nil), common...), step.fields...)
		if !reflect.DeepEqual(labels, want) {
			t.Errorf("ORPHAN_SETUP_ROW: %s rows = %v; want %v", step.mode, labels, want)
		}
	}
	var trust *widget.Select
	for _, item := range form.Items {
		if item.Text == "证书信任" {
			trust, _ = item.Widget.(*widget.Select)
		}
	}
	if trust == nil {
		t.Fatal("explicit trust selector is absent")
	}
	trust.SetSelected("固定证书指纹")
	if form.Items[len(form.Items)-1].Text != "可信证书指纹" {
		t.Fatal("pin mode hides required fingerprint")
	}
	trust.SetSelected("系统证书验证")
	for _, item := range form.Items {
		if item.Text == "可信证书指纹" {
			t.Fatal("system verification retains an irrelevant pin input")
		}
	}
}

func TestGUISetupPrimaryActionAndLocation(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()
	window := application.NewWindow("Agent setup")
	defer window.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	showGUISetup(context.Background(), window, path, func(config, string) { t.Fatal("setup started hardware") })
	primary, location := false, false
	for _, object := range test.LaidOutObjects(window.Content()) {
		if button, ok := object.(*widget.Button); ok && button.Text == "验证并保存" {
			primary = button.Importance == widget.HighImportance && !button.Disabled()
		}
		if section, ok := object.(*widget.Accordion); ok {
			for _, item := range section.Items {
				if item.Title == "配置位置" {
					location = !item.Open
				}
			}
		}
		if label, ok := object.(*widget.Label); ok && strings.Contains(label.Text, path) && label.Visible() {
			t.Fatal("first setup exposes the path as primary page content")
		}
	}
	if !primary || !location {
		t.Fatalf("GUI_SETUP_USABILITY: primary=%t advanced-location=%t", primary, location)
	}
}
