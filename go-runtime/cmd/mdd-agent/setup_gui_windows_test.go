//go:build gui && windows

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestGUISetupCanChooseExistingConfigBesidePending(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("ProgramData", t.TempDir())
	t.Setenv("MDD_AGENT_CONFIG", "")
	paths, err := configLocations()
	if err != nil || len(paths) != 2 {
		t.Fatalf("isolated Windows locations: %v %v", paths, err)
	}
	var requests atomic.Int32
	endpoint := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "selection must not connect", http.StatusServiceUnavailable)
	})
	serverA, serverB := httptest.NewTLSServer(endpoint), httptest.NewTLSServer(endpoint)
	defer serverA.Close()
	defer serverB.Close()
	tx, err := beginSetup(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer tx.close()
	if err := tx.configure(setupInput{Server: serverA.URL, TLSMode: "pin", Fingerprint: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	tx.pending.Requested = true
	if err := tx.savePending(); err != nil {
		t.Fatal(err)
	}
	tx.close()
	pendingBefore, err := os.ReadFile(paths[0] + ".setup-pending")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := newEditableConfig()
	if err != nil {
		t.Fatal(err)
	}
	settings.Agent.ServerURL = strings.Replace(serverB.URL, "https:", "wss:", 1) + "/v1/agent/ws"
	settings.Agent.ServerToken = strings.Repeat("b", 32)
	settings.Agent.TLSMode = "system"
	if err := saveConfig(paths[1], settings); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(paths[1]); err != nil {
		t.Fatalf("existing configuration fixture is not complete: %v", err)
	}
	configBefore, err := os.ReadFile(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	application := test.NewApp()
	defer application.Quit()
	window := application.NewWindow("Choose existing Agent")
	defer window.Close()
	window.Resize(fyne.NewSize(900, 800))
	starts := 0
	showGUISetup(t.Context(), window, "", func(config, string) { starts++ })
	fields := func() map[string]fyne.CanvasObject {
		found := map[string]fyne.CanvasObject{}
		for _, object := range test.LaidOutObjects(window.Content()) {
			if form, ok := object.(*widget.Form); ok {
				for _, item := range form.Items {
					found[item.Text] = item.Widget
				}
			}
		}
		return found
	}
	initial := fields()
	choice, ok := initial["已发现路径"].(*widget.Select)
	if !ok || len(choice.Options) != 2 {
		t.Fatal("multiple configuration selector is absent")
	}
	pathEntry, ok := initial["配置文件（绝对路径）"].(*widget.Entry)
	if !ok {
		t.Fatal("configuration path input is absent")
	}
	if pathEntry.Text != "" {
		t.Error("multiple configurations silently selected the first owner")
	}
	choose := func(index int) {
		t.Helper()
		if choice.Disabled() {
			t.Fatal("pending enrollment disables selection of another configuration")
		}
		test.Tap(choice)
		menu, ok := window.Canvas().Focused().(*widget.PopUpMenu)
		if !ok {
			t.Fatal("configuration selector did not open its menu")
		}
		for range index + 1 {
			menu.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
		}
		menu.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
		if filepath.Clean(pathEntry.Text) != paths[index] {
			t.Fatal("menu did not select the requested configuration")
		}
	}
	choose(0)
	bound := fields()
	server, ok := bound["Core 地址"].(*widget.Entry)
	if !ok || !server.Disabled() || server.Text != tx.pending.Settings.Agent.ServerURL {
		t.Fatal("pending endpoint is not restored and frozen")
	}
	bound["网页密码（不保存）"].(*widget.Entry).SetText("pending-only-password")
	choose(1)
	selected := fields()
	server = selected["Core 地址"].(*widget.Entry)
	trust := selected["证书信任"].(*widget.Select)
	mode := selected["接入方式"].(*widget.Select)
	password := selected["网页密码（不保存）"].(*widget.Entry)
	if server.Disabled() || trust.Disabled() || mode.Disabled() || server.Text != settings.Agent.ServerURL || trust.Selected != "系统证书验证" || password.Text != "" {
		t.Fatal("pending fields or disabled state leaked into the selected configuration")
	}
	mode.SetSelected("已有 Agent 凭据")
	selected = fields()
	if selected["Agent ID"].(*widget.Entry).Text != settings.Agent.ID || selected["Agent token"].(*widget.Entry).Text != "" {
		t.Fatal("selected configuration retained another identity or exposed credentials")
	}
	pendingAfter, pendingErr := os.ReadFile(paths[0] + ".setup-pending")
	configAfter, configErr := os.ReadFile(paths[1])
	if pendingErr != nil || configErr != nil || !bytes.Equal(pendingBefore, pendingAfter) || !bytes.Equal(configBefore, configAfter) || requests.Load() != 0 || starts != 0 {
		t.Fatalf("selection changed files or started work: pending=%v config=%v requests=%d starts=%d", pendingErr, configErr, requests.Load(), starts)
	}
}
