//go:build gui && windows

package main

import (
	"os"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// Runs inside the restricted ordinary-user process from the real SCM journey.
func TestManagementGUIHelper(t *testing.T) {
	if os.Getenv("MDD_MANAGEMENT_HELPER") != "edit" {
		return
	}
	installed, err := installedManagementService()
	if err != nil {
		t.Fatal(err)
	}
	client := &windowsManagementClient{installation: installed}
	state, err := client.request(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	application := test.NewApp()
	defer application.Quit()
	window := application.NewWindow("Existing service")
	defer window.Close()
	controller := &guiController{ctx: t.Context(), window: window, settings: state.Running.guiConfig(), configPath: installed.path, serviceClient: client}
	window.SetContent(controller.content())
	controller.loadSnapshot()
	if !strings.Contains(controller.runningInfo.Text, state.Running.AgentID) || !strings.Contains(controller.savedInfo.Text, state.Saved.Server) {
		t.Fatal("managed GUI lost the running/saved identity")
	}
	if controller.settings.Agent.ServerToken != "" || controller.settings.Control.Token != "" {
		t.Fatal("GUI received a service secret")
	}
	for _, object := range test.LaidOutObjects(controller.buttons()) {
		if _, ok := object.(*widget.Button); ok {
			t.Fatal("ordinary GUI exposes privileged runtime operations")
		}
	}
	if !controller.reserveAction() {
		t.Fatal("editor not available")
	}
	controller.connectionDialogManaged(connectionEdit{path: installed.path, settings: state.Saved.guiConfig()}, &state)
	var closeEditor *widget.Button
	foundToken, foundServer := false, false
	for _, overlay := range window.Canvas().Overlays().List() {
		for _, object := range test.LaidOutObjects(overlay) {
			if form, ok := object.(*widget.Form); ok {
				for _, item := range form.Items {
					switch item.Text {
					case "替换 Agent token":
						entry := item.Widget.(*widget.Entry)
						if entry.Text != "" || !entry.Password {
							t.Fatal("credential field was not empty and masked")
						}
						foundToken = true
					case "Core 地址":
						if item.Widget.(*widget.Entry).Text != state.Saved.Server {
							t.Fatal("editor uses running rather than saved destination")
						}
						foundServer = true
					}
				}
			}
			if button, ok := object.(*widget.Button); ok && button.Text == "关闭" {
				closeEditor = button
			}
		}
	}
	if !foundToken || !foundServer || closeEditor == nil {
		t.Fatal("managed editor controls missing")
	}
	test.Tap(closeEditor)
	after, err := client.request(t.Context(), nil)
	if err != nil || after.Revision != state.Revision || after.Session != state.Session || controller.actionBusy {
		t.Fatal("closing editor modified or retained service ownership")
	}
}
