//go:build gui && (darwin || windows)

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/lang"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawcapture"
)

func TestGUIOverviewSeparatesRuntimeConnectionAndDevices(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()
	window := application.NewWindow("MDD Agent")
	defer window.Close()
	controller := &guiController{ctx: t.Context(), window: window, refresh: make(chan struct{}, 1)}
	window.SetContent(controller.content())
	var tabs *container.AppTabs
	for _, object := range test.LaidOutObjects(window.Content()) {
		if found, ok := object.(*container.AppTabs); ok {
			tabs = found
			break
		}
	}
	if tabs == nil || len(tabs.Items) != 3 {
		t.Fatal("overview/configuration/diagnostics navigation absent")
	}
	for _, item := range tabs.Items {
		tabs.Select(item)
		if tabs.Selected() != item {
			t.Fatal("tab selection stayed on another view")
		}
	}
	for _, step := range []struct {
		state             agentcontrol.State
		link              string
		local, connection widget.Importance
	}{
		{agentcontrol.StateRunning, "connected", widget.SuccessImportance, widget.SuccessImportance},
		{agentcontrol.StateRunning, "retrying", widget.SuccessImportance, widget.WarningImportance},
		{agentcontrol.StateRunning, "unknown", widget.SuccessImportance, widget.WarningImportance},
		{agentcontrol.StateRunning, "disconnected", widget.SuccessImportance, widget.DangerImportance},
		{agentcontrol.StateStopped, "connected", widget.MediumImportance, widget.MediumImportance},
		{agentcontrol.StateFailed, "connected", widget.DangerImportance, widget.MediumImportance},
	} {
		controller.renderSnapshot(map[string]any{"runtime": agentcontrol.Snapshot{State: step.state, CoreConnection: &agentcontrol.CoreConnection{State: step.link}}})
		if controller.summary.Importance != step.local || controller.connection.Importance != step.connection {
			t.Errorf("%s/%s health conflated: local=%d connection=%d", step.state, step.link, controller.summary.Importance, controller.connection.Importance)
		}
	}
	controller.renderSnapshot(map[string]any{"topology": agentlink.TopologySnapshot{
		ReaderCondition: agentlink.ReaderReady, ModemCondition: agentlink.ModemDisabled,
		Readers: []agentlink.ReaderFact{{ReaderName: "fixture-reader", CardPresent: true, CardID: "8900000000000000001", IdentityState: agentlink.CardIdentified}},
	}})
	if !strings.Contains(guiVisibleText(controller.devices), "卡片已识别") {
		t.Fatal("identified card missing from overview")
	}
	controller.renderSnapshot(map[string]any{"topology_error": "gone"})
	text := guiVisibleText(controller.devices)
	if strings.Contains(text, "fixture-reader") || !strings.Contains(text, "不可读取") {
		t.Fatal("old green topology remained after read failure")
	}
	for _, value := range []string{"off", "disabled", "disconnected", "unknown", "", "unmanaged"} {
		if guiStateImportance(value) == widget.SuccessImportance {
			t.Fatalf("%q displayed as healthy", value)
		}
	}
}

func TestGUIDiagnosticsAndSavedConfigDoNotPublishSecrets(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()
	window := application.NewWindow("MDD Agent")
	defer window.Close()
	settings, err := newEditableConfig()
	if err != nil {
		t.Fatal(err)
	}
	settings.Agent.ServerToken = "private-token-do-not-copy"
	settings.Agent.ServerURL = "wss://private-host.example/v1/agent/ws"
	settings.Agent.TLSMode = "pin"
	settings.Agent.TLSFingerprint = "private-fingerprint"
	controller := &guiController{ctx: t.Context(), window: window, settings: settings}
	window.SetContent(controller.content())
	value := map[string]any{
		"runtime":       agentcontrol.Snapshot{State: agentcontrol.StateRunning, Detail: "private-runtime-detail", CoreConnection: &agentcontrol.CoreConnection{State: "retrying"}},
		"runtime_error": "private-http-error",
		"topology": agentlink.TopologySnapshot{
			Readers: []agentlink.ReaderFact{{ReaderName: "private-reader", CardID: "private-iccid"}},
			Modems:  []agentlink.ModemFact{{EquipmentID: "private-imei", SIM: agentlink.ModemSIMFact{ICCID: "private-iccid", IMSI: "private-imsi", MSISDNs: []string{"private-phone"}}, Network: agentlink.ModemNetworkFact{APN: "private-apn", Address: "private-address", Data: "disconnected", DataGuard: "protected"}}},
		},
		"saved_config": guiSavedConnection(settings, settings),
	}
	controller.renderSnapshot(value)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(controller.diagnosticText), &decoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(controller.diagnosticText, "private-") || decoded["reader_count"] != float64(1) || decoded["runtime_state"] != "running" {
		t.Fatal("diagnostic export leaked identity or lost operational states")
	}
	controller.details.SetText("user edit")
	if controller.details.Text != controller.diagnosticText || controller.details.Disabled() {
		t.Fatal("diagnostics are not readable/selectable and immutable")
	}
	for _, secret := range []string{settings.Agent.ServerToken, settings.Agent.TLSFingerprint, settings.Control.Token} {
		if strings.Contains(controller.savedInfo.Text, secret) {
			t.Fatal("saved configuration exposes credential")
		}
	}
	changed := settings
	changed.Agent.ServerURL = "wss://new-target.example/v1/agent/ws"
	value["saved_config"] = guiSavedConnection(changed, settings)
	controller.renderSnapshot(value)
	if !strings.Contains(controller.savedInfo.Text, "new-target.example") || !strings.Contains(controller.savedStatus.Text, "尚未确认") || controller.settings.Agent.ServerURL != settings.Agent.ServerURL {
		t.Fatal("disk configuration was not distinguished from the running connection")
	}
}

func TestGUIActionCancellationDoesNotDispatch(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()
	window := application.NewWindow("MDD Agent")
	defer window.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	controller := &guiController{ctx: ctx, window: window}
	window.SetContent(controller.content())
	dispatched := false
	controller.confirmAction("Confirm", "Stop hardware", nil, func(context.Context) error { dispatched = true; return nil })
	if !controller.actionBusy || controller.reserveAction() {
		t.Fatal("second operation admitted during confirmation")
	}
	for _, button := range controller.actionButtons {
		if !button.Disabled() {
			t.Fatal("mutating action remained enabled")
		}
	}
	cancelled := false
	for _, overlay := range window.Canvas().Overlays().List() {
		for _, object := range test.LaidOutObjects(overlay) {
			if button, ok := object.(*widget.Button); ok && button.Text == lang.L("No") {
				test.Tap(button)
				cancelled = true
				break
			}
		}
	}
	if !cancelled || dispatched || controller.actionBusy {
		t.Fatalf("cancel did not release operation without dispatch: found=%t dispatched=%t reserved=%t", cancelled, dispatched, controller.actionBusy)
	}
	for _, button := range controller.actionButtons {
		if button.Disabled() {
			t.Fatal("cancel left actions disabled")
		}
	}
	cancel()
	if controller.reserveAction() {
		t.Fatal("closed controller accepted another operation")
	}
}

func guiVisibleText(root fyne.CanvasObject) string {
	var values []string
	for _, object := range test.LaidOutObjects(root) {
		if label, ok := object.(*widget.Label); ok {
			values = append(values, label.Text)
		}
	}
	return strings.Join(values, "\n")
}

func TestGUIPersistentModesRemainVisibleWithoutSessions(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()
	window := application.NewWindow("MDD Agent")
	defer window.Close()
	controller := &guiController{ctx: t.Context(), window: window}
	window.SetContent(controller.content())
	adapted := rawcapture.Pair{EquipmentID: "private-adapted-device", CardID: "private-adapted-card"}
	legacy := rawcapture.Pair{EquipmentID: "private-raw-device", CardID: "private-raw-card"}
	controller.renderSnapshot(map[string]any{"raw_modes": rawcapture.Snapshot{
		ModeSelections: []rawcapture.ModeSelection{{Pair: adapted, Mode: "adapted"}},
		Desired:        []rawcapture.Pair{legacy},
		Captures:       []rawcapture.Record{{Pair: legacy, Stage: rawcapture.StageReleasePending}},
	}})
	text := guiVisibleText(controller.rawModes)
	for _, expected := range []string{adapted.EquipmentID, adapted.CardID, legacy.EquipmentID, legacy.CardID, "本机适配", "USB 穿透", "等待清理；隔离保留"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("offline retained state missing: %s", expected)
		}
	}
	for _, object := range test.LaidOutObjects(controller.rawModes) {
		if label, ok := object.(*widget.Label); ok && label.Importance == widget.SuccessImportance {
			t.Fatal("desired state presented as live readiness")
		}
	}
	var diagnostic map[string]any
	if err := json.Unmarshal([]byte(controller.diagnosticText), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(controller.diagnosticText, "private-") || diagnostic["retained_capture_count"] != float64(1) {
		t.Fatal("copied diagnostic leaked identity or omitted retained state")
	}
	controller.renderSnapshot(map[string]any{"raw_modes_error": "private-error"})
	text = guiVisibleText(controller.rawModes)
	if strings.Contains(text, "private-") || !strings.Contains(text, "不可读取") {
		t.Fatal("read failure retained stale selection")
	}
}
