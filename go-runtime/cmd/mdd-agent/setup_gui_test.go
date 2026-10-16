//go:build gui && (darwin || windows)

package main

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
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
	for _, child := range window.Content().(*fyne.Container).Objects {
		if scroll, ok := child.(*container.Scroll); ok {
			form, _ = scroll.Content.(*widget.Form)
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
	common := []string{"已发现路径", "配置文件（绝对路径）", "接入方式"}
	for _, step := range []struct {
		mode   string
		fields []string
	}{
		{"网页登录", []string{"Core 地址", "可信证书指纹（可选）", "网页用户名", "网页密码（不保存）"}},
		{"接入包", []string{"接入 JSON"}},
		{"已有 Agent 凭据", []string{"Core 地址", "可信证书指纹（可选）", "Agent ID", "Agent token"}},
		{"网页登录", []string{"Core 地址", "可信证书指纹（可选）", "网页用户名", "网页密码（不保存）"}},
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
}
