//go:build gui && (darwin || windows)

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
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

func TestGUISetupResumesPinnedPending(t *testing.T) {
	for _, lost := range []string{"create-receipt", "credential-check"} {
		t.Run(lost, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			payload := `{"version":1,"username":"setup-user","salt":"00112233445566778899aabbccddeeff","password_hash":"ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302","agent_token":"0123456789abcdef0123456789abcdef"}`
			if err := os.WriteFile(authPath, []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
			manager, err := adminauth.NewManager(authPath, true, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := adminauth.NewHandler(manager)
			if err != nil {
				t.Fatal(err)
			}
			var lose atomic.Bool
			lose.Store(true)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if lose.Load() && ((lost == "create-receipt" && r.URL.Path == "/api/auth/agent-credentials") || (lost == "credential-check" && r.URL.Path == "/api/auth/agent-credentials/check")) {
					handler.ServeHTTP(httptest.NewRecorder(), r)
					http.Error(w, "lost reply", http.StatusServiceUnavailable)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			digest := sha256.Sum256(server.Certificate().Raw)
			pin := hex.EncodeToString(digest[:])
			path := filepath.Join(t.TempDir(), "config.json")
			tx, err := beginSetup(path)
			if err != nil {
				t.Fatal(err)
			}
			input := setupInput{Server: server.URL, TLSMode: "pin", Fingerprint: pin, Username: "setup-user", Password: "correct horse battery staple"}
			_, _, err = tx.enroll(t.Context(), input)
			original := tx.pending
			tx.close()
			if err == nil || !original.Requested || len(manager.AgentCredentials().Active) != 1 {
				t.Fatal("fixture did not retain an issued, unfinished enrollment")
			}
			lose.Store(false)
			application := test.NewApp()
			defer application.Quit()
			window := application.NewWindow("Resume setup")
			defer window.Close()
			showGUISetup(t.Context(), window, path, func(config, string) { t.Error("pending enrollment started hardware") })
			fields := map[string]any{}
			for _, object := range test.LaidOutObjects(window.Content()) {
				if form, ok := object.(*widget.Form); ok {
					for _, item := range form.Items {
						fields[item.Text] = item.Widget
					}
				}
			}
			serverInput, okServer := fields["Core 地址"].(*widget.Entry)
			pinInput, okPin := fields["可信证书指纹"].(*widget.Entry)
			trust, okTrust := fields["证书信任"].(*widget.Select)
			mode, okMode := fields["接入方式"].(*widget.Select)
			if !okServer || !okPin || !okTrust || !okMode || serverInput.Text != original.Settings.Agent.ServerURL || pinInput.Text != pin || trust.Selected != "固定证书指纹" {
				t.Fatal("reopened GUI did not restore bound endpoint and trust")
			}
			if !serverInput.Disabled() || !pinInput.Disabled() || !trust.Disabled() || !mode.Disabled() {
				t.Fatal("reopened GUI permits replacing requested enrollment identity")
			}
			input.Server, input.Fingerprint = serverInput.Text, pinInput.Text
			input.TLSMode = "pin"
			if lost == "credential-check" {
				if mode.Selected != "已有 Agent 凭据" {
					t.Fatal("saved credential asks for another enrollment")
				}
				input.Username, input.Password = "", ""
			} else if mode.Selected != "网页登录" {
				t.Fatal("lost create receipt cannot reauthenticate")
			}
			tx, err = beginSetup(path)
			if err != nil {
				t.Fatal(err)
			}
			if tx.pending.EnrollmentID != original.EnrollmentID {
				t.Fatal("resume allocated another request")
			}
			settings, warning, err := tx.enroll(t.Context(), input)
			tx.close()
			if err != nil || warning != "" || settings.Agent.ID != original.Settings.Agent.ID || settings.Control.Token != original.Settings.Control.Token || len(manager.AgentCredentials().Active) != 1 {
				t.Fatalf("resume changed owner or failed: %v %s", err, warning)
			}
			if _, err := os.Stat(path + ".setup-pending"); !os.IsNotExist(err) {
				t.Fatal("successful resume retained pending credentials")
			}
		})
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
