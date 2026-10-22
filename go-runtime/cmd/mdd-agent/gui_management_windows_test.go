//go:build gui && windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawcapture"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawusb"
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
	for _, scenario := range []struct {
		name, environment, requested, rejection string
	}{
		{"environment-other", filepath.Join(filepath.Dir(installed.path), "other.json"), "", "指定配置与现有服务不一致"},
		{"environment-relative", "relative.json", "", "指定配置路径无效"},
		{"explicit-precedence", "relative.json", installed.path, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("MDD_AGENT_CONFIG", scenario.environment)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opened := make(chan string, 1)
			if !showInstalledServiceGUI(ctx, window, scenario.requested, func(_ config, path string, _ guiServiceClient) { opened <- path }) {
				t.Fatal("MANAGEMENT_TARGET: installed service fell through to setup")
			}
			if scenario.rejection != "" {
				var text strings.Builder
				for _, object := range test.LaidOutObjects(window.Content()) {
					if label, ok := object.(*widget.Label); ok {
						text.WriteString(label.Text)
					}
				}
				if !strings.Contains(text.String(), scenario.rejection) {
					t.Fatal("MANAGEMENT_TARGET: explicit environment target was not rejected")
				}
				return
			}
			select {
			case path := <-opened:
				if !sameWindowsPath(path, installed.path) {
					t.Fatal("MANAGEMENT_TARGET: opened a different service")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("MANAGEMENT_TARGET: explicit argument did not override environment")
			}
		})
	}
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

type managementStoredModes struct {
	processWorker
	store *rawcapture.Store
}

func (source managementStoredModes) RawModeSnapshot() (rawcapture.Snapshot, error) {
	return source.store.Snapshot()
}

func (managementStoredModes) SetRawMode(rawcapture.Pair, bool) error {
	return errors.New("read-only fixture")
}

// Only the transport is replaced: the real management handler reads the real
// control API and durable store, then the real GUI renders its JSON response.
type managementHTTPClient struct{ url string }

func (client managementHTTPClient) request(ctx context.Context, edit *localManagementEdit) (localManagementState, error) {
	var state localManagementState
	if edit != nil {
		return state, errors.New("read-only fixture")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.url+"/v1/connection", nil)
	if err != nil {
		return state, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return state, err
	}
	defer response.Body.Close()
	err = json.NewDecoder(response.Body).Decode(&state)
	return state, err
}

func TestManagementPersistedModesReadback(t *testing.T) {
	store, err := rawcapture.Open(filepath.Join(t.TempDir(), "modes.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	pair := rawcapture.Pair{EquipmentID: "860000000000001", CardID: "89010000000000000001"}
	if err := store.SetRaw(pair); err != nil {
		t.Fatal(err)
	}
	proof := rawcapture.Proof{Pair: pair, AttachmentID: "offline-attachment", SessionGeneration: "old-session", PhysicalID: "private-physical-path"}
	if _, err := store.ArmCapture(proof, "old-capture", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteCapture(pair, "old-capture", rawusb.Device{BusID: "old-bus", VendorID: 1, ProductID: 2}, time.Now()); err != nil {
		t.Fatal(err)
	}
	source := managementStoredModes{store: store}
	runtime, err := agentcontrol.New(source, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop(context.Background())
	settings := testConfig(t)
	api, err := agentcontrol.NewAPI(runtime, settings.Control.Token, time.Second, source)
	if err != nil {
		t.Fatal(err)
	}
	control := httptest.NewServer(api)
	defer control.Close()
	settings.Control.Listen = strings.TrimPrefix(control.URL, "http://")
	settings.configPath = filepath.Join(t.TempDir(), "agent.json")
	if err := saveConfig(settings.configPath, settings); err != nil {
		t.Fatal(err)
	}
	managed := httptest.NewServer(&localManagementHandler{settings: settings, session: strings.Repeat("b", 32), slots: make(chan struct{}, 4), authorize: func() error { return nil }})
	defer managed.Close()
	client := managementHTTPClient{url: managed.URL}
	state, err := client.request(t.Context(), nil)
	if err != nil || state.RawModes == nil || state.RawModesUnavailable {
		t.Fatal("MANAGEMENT_MODES: offline durable modes absent from service readback", err)
	}
	if state.Topology == nil || len(state.Topology.RawUSBSessions) != 0 || len(state.Topology.Modems) != 0 {
		t.Fatal("fixture must have no current modem or raw session")
	}
	payload, _ := json.Marshal(state)
	for _, private := range []string{proof.PhysicalID, proof.AttachmentID, proof.SessionGeneration, "old-bus", "old-capture", settings.Control.Token} {
		if strings.Contains(string(payload), private) {
			t.Fatal("read-only projection exposed capture internals")
		}
	}
	application := test.NewApp()
	defer application.Quit()
	window := application.NewWindow("Persisted modes")
	defer window.Close()
	controller := &guiController{ctx: t.Context(), window: window, settings: state.Running.guiConfig(), serviceClient: client}
	window.SetContent(controller.content())
	controller.loadSnapshot()
	modeText := func() string {
		var text strings.Builder
		for _, object := range test.LaidOutObjects(controller.rawModes) {
			if label, ok := object.(*widget.Label); ok {
				text.WriteString(label.Text)
			}
			if _, ok := object.(*widget.Button); ok {
				t.Fatal("managed mode view must remain read-only")
			}
		}
		return text.String()
	}
	for _, expected := range []string{pair.EquipmentID, pair.CardID, "已保存：USB 穿透", "已保留设备占用"} {
		if controller.rawModes.Hidden || !strings.Contains(modeText(), expected) {
			t.Fatal("MANAGEMENT_MODES: offline choice or retained capture hidden")
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	controller.loadSnapshot()
	if strings.Contains(modeText(), pair.CardID) || !strings.Contains(modeText(), "已保存模式不可读取") {
		t.Fatal("failed read left stale saved choices visible")
	}
}

var _ guiServiceClient = managementHTTPClient{}
