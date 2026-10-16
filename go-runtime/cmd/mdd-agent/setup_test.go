package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/pintls"
)

func TestAgentSetupFlow(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	payload := `{"version":1,"username":"setup-user","salt":"00112233445566778899aabbccddeeff","password_hash":"ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302","agent_token":"0123456789abcdef0123456789abcdef"}`
	if err := os.WriteFile(authPath, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := adminauth.NewManager(authPath, true, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := adminauth.NewHandler(manager)
	var credentialRequests atomic.Int32
	var session atomic.Value
	var loseReceipt atomic.Bool
	var invalidCheck atomic.Bool
	var blockCreate atomic.Bool
	createEntered := make(chan struct{}, 1)
	var blockLogout atomic.Bool
	logoutEntered, releaseLogout := make(chan struct{}, 1), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			credentialRequests.Add(1)
		}
		if r.URL.Path == "/api/auth/login" {
			receipt := httptest.NewRecorder()
			handler.ServeHTTP(receipt, r)
			var value struct {
				Token string `json:"token"`
			}
			_ = json.Unmarshal(receipt.Body.Bytes(), &value)
			session.Store(value.Token)
			w.WriteHeader(receipt.Code)
			_, _ = w.Write(receipt.Body.Bytes())
			return
		}
		if r.URL.Path == "/api/auth/logout" && blockLogout.Load() {
			receipt := httptest.NewRecorder()
			handler.ServeHTTP(receipt, r)
			logoutEntered <- struct{}{}
			select {
			case <-releaseLogout:
			case <-r.Context().Done():
			}
			w.WriteHeader(receipt.Code)
			_, _ = w.Write(receipt.Body.Bytes())
			return
		}
		if r.URL.Path == "/api/auth/agent-credentials" && blockCreate.Load() {
			handler.ServeHTTP(httptest.NewRecorder(), r)
			createEntered <- struct{}{}
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/api/auth/agent-credentials" && loseReceipt.Load() {
			receipt := httptest.NewRecorder()
			handler.ServeHTTP(receipt, r)
			http.Error(w, "lost reply", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/auth/agent-credentials/check" && invalidCheck.Load() {
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	digest := sha256.Sum256(server.Certificate().Raw)
	pin := hex.EncodeToString(digest[:])
	input := setupInput{Server: server.URL, Fingerprint: pin, Username: "setup-user", Password: "correct horse battery staple"}
	path := filepath.Join(t.TempDir(), "agent", "config.json")
	tx, err := beginSetup(path)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := beginSetup(path); err == nil {
		other.close()
		t.Fatal("DUPLICATE_SETUP_OWNER")
	}
	if err := runConfigCommand([]string{"set", "-config", path, "agent_id", "other-agent"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("CONFIG_SET_BYPASSED_SETUP_LOCK")
	}
	originalID, control := tx.pending.Settings.Agent.ID, tx.pending.Settings.Control.Token
	loseReceipt.Store(true)
	if _, _, err := tx.enroll(context.Background(), input); err == nil {
		t.Fatal("lost create response was reported successful")
	}
	tx.close()
	if _, found := manager.Session(session.Load().(string)); found {
		t.Fatal("ADMIN_SESSION_LEAK")
	}
	if len(manager.AgentCredentials().Active) != 1 {
		t.Fatal("create did not persist exactly once")
	}
	tx, err = beginSetup(path)
	if err != nil {
		t.Fatal(err)
	}
	loseReceipt.Store(false)
	settings, warning, err := tx.enroll(context.Background(), input)
	tx.close()
	if err != nil || warning != "" {
		t.Fatalf("resume: %v %s", err, warning)
	}
	loaded, err := loadConfig(path)
	if err != nil || loaded.Agent.ID != originalID || loaded.Control.Token != control || len(manager.AgentCredentials().Active) != 1 {
		t.Fatal("RESUME_CHANGED_IDENTITY")
	}
	if settings.Agent.ModemEnabled || settings.Agent.RawUSBSource || settings.Agent.RawUSBImporter || len(settings.Agent.PINs) != 0 {
		t.Fatal("SETUP_CHANGED_HARDWARE_POLICY")
	}
	if _, found := manager.Session(session.Load().(string)); found {
		t.Fatal("ADMIN_SESSION_LEAK")
	}
	if _, err := os.Stat(path + ".setup-pending"); !os.IsNotExist(err) {
		t.Fatal("successful setup retained pending secrets")
	}

	// The live endpoint sees no credentials under the wrong pin or an untrusted CA.
	for _, mode := range []string{"pin", "system"} {
		otherPath := filepath.Join(t.TempDir(), "config.json")
		other, _ := beginSetup(otherPath)
		bad := input
		bad.Fingerprint = strings.Repeat("0", 64)
		if mode == "system" {
			bad.Fingerprint = ""
		}
		before := credentialRequests.Load()
		if _, _, err := other.enroll(context.Background(), bad); err == nil {
			t.Fatalf("TLS_DOWNGRADE: %s", mode)
		}
		other.close()
		if before != credentialRequests.Load() {
			t.Fatal("CREDENTIAL_SENT_BEFORE_TRUST")
		}
	}
	client, err := pintls.NewPolicyHTTPClient(strings.Replace(server.URL, "https:", "wss:", 1)+"/v1/agent/ws", "system", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	if client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("system mode bypasses verification")
	}
	response, err := client.Get(server.URL + "/api/auth/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	client.CloseIdleConnections()
	if _, err = pintls.NewPolicyHTTPClient(settings.Agent.ServerURL, "system", pin, time.Second); err == nil {
		t.Fatal("conflicting trust silently accepted")
	}
	if _, err = pintls.NewHTTPClient(settings.Agent.ServerURL, "", time.Second); err == nil {
		t.Fatal("legacy empty pin became system mode")
	}

	// Continue an init draft without changing switches, keys, or existing stores.
	draftPath := filepath.Join(t.TempDir(), "config.json")
	draft, _ := newEditableConfig()
	draft.Agent.AdvertiseHost = "display-only"
	if err := saveConfig(draftPath, draft); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(filepath.Dir(draftPath), "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(state, "retained.db")
	_ = os.WriteFile(marker, []byte("retained"), 0600)
	tx, err = beginSetup(draftPath)
	if err != nil {
		t.Fatal(err)
	}
	settings, _, err = tx.enroll(context.Background(), input)
	tx.close()
	if err != nil || settings.Agent.ID != draft.Agent.ID || settings.Control.Token != draft.Control.Token || settings.Agent.AdvertiseHost != "display-only" {
		t.Fatalf("DRAFT_REPLACED: %v", err)
	}
	if data, _ := os.ReadFile(marker); string(data) != "retained" {
		t.Fatal("STATE_CHANGED")
	}
	packageBytes, _ := json.Marshal(agentConnectionPackage{Kind: "mdd-agent-connection", Version: 1,
		AgentID: settings.Agent.ID, ServerURL: settings.Agent.ServerURL, ServerToken: settings.Agent.ServerToken,
		TLSMode: settings.Agent.TLSMode, TLSFingerprint: settings.Agent.TLSFingerprint})
	importPath := filepath.Join(t.TempDir(), "config.json")
	importTx, err := beginSetup(importPath)
	if err != nil {
		t.Fatal(err)
	}
	count := len(manager.AgentCredentials().Active)
	invalidCheck.Store(true)
	if _, _, err := importTx.enroll(context.Background(), setupInput{Package: string(packageBytes)}); err == nil {
		t.Fatal("INVALID_CREDENTIAL_CHECK_ACCEPTED")
	}
	invalidCheck.Store(false)
	imported, _, err := importTx.enroll(context.Background(), setupInput{Package: string(packageBytes)})
	if err == nil {
		// Simulate exit after final publication but before pending cleanup.
		err = importTx.savePending()
	}
	importTx.close()
	if err != nil || imported.Agent.ID != settings.Agent.ID || len(manager.AgentCredentials().Active) != count || imported.Control.Token == settings.Control.Token {
		t.Fatalf("PACKAGE_IMPORT_CHANGED_SERVER_IDENTITY_OR_LOCAL_SECRET: %v", err)
	}
	recovered, err := beginSetup(importPath)
	if err != nil {
		t.Fatalf("OWN_PUBLICATION_NOT_RECOVERED: %v", err)
	}
	_, err = recovered.publish()
	recovered.close()
	if err != nil {
		t.Fatalf("OWN_PUBLICATION_NOT_RECOVERED: %v", err)
	}
	var redirected atomic.Int32
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	redirectDigest := sha256.Sum256(redirect.Certificate().Raw)
	redirectTx, err := beginSetup(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = redirectTx.enroll(context.Background(), setupInput{Server: redirect.URL, Fingerprint: hex.EncodeToString(redirectDigest[:]), Username: input.Username, Password: input.Password})
	redirectTx.close()
	if err == nil || redirected.Load() != 0 {
		t.Fatal("SETUP_FOLLOWED_REDIRECT")
	}
	runtimeURL := strings.Replace(redirect.URL, "https:", "wss:", 1) + "/v1/agent/ws"
	runtimeClient, err := pintls.NewPolicyHTTPClient(runtimeURL, "pin", hex.EncodeToString(redirectDigest[:]), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mediaSocket, _, err := websocket.Dial(t.Context(), runtimeURL, &websocket.DialOptions{
		HTTPClient: runtimeClient,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + settings.Agent.ServerToken}, "X-MDD-Stream-Token": {"synthetic-stream-token"}},
	})
	if mediaSocket != nil {
		mediaSocket.CloseNow()
	}
	runtimeClient.CloseIdleConnections()
	if err == nil || redirected.Load() != 0 {
		t.Fatal("RUNTIME_FORWARDED_CREDENTIALS")
	}

	// A target created while the user is entering credentials must survive.
	conflict := filepath.Join(t.TempDir(), "config.json")
	tx, err = beginSetup(conflict)
	if err != nil {
		t.Fatal(err)
	}
	tx.pending.Settings = settings
	_ = os.WriteFile(conflict, []byte("another owner"), 0600)
	if _, err := tx.publish(); err == nil {
		t.Fatal("OVERWROTE_NEW_CONFIG")
	}
	tx.close()
	if data, _ := os.ReadFile(conflict); string(data) != "another owner" {
		t.Fatal("OVERWROTE_NEW_CONFIG")
	}
	var output bytes.Buffer
	if err := runConfigCommand([]string{"setup", "-config", conflict}, strings.NewReader(""), &output); err == nil {
		t.Fatal("NONINTERACTIVE_SETUP_WAITED")
	}
	if err := strictSetupJSON([]byte(`{"kind":"mdd-agent-connection","version":1,"control":{"token":"injected"}}`), &agentConnectionPackage{}); err == nil {
		t.Fatal("PACKAGE_INJECTED_LOCAL_POLICY")
	}
	if executable := os.Getenv("MDD_AGENT_SETUP_BINARY"); executable != "" {
		for _, interruption := range []string{"none", "create", "logout"} {
			interrupted := interruption != "none"
			cliPath := filepath.Join(t.TempDir(), "config.json")
			process := exec.Command("expect", "-c", `
set timeout 30
if {$env(MDD_SETUP_PHASE) eq "logout"} {
  spawn -noecho $env(MDD_SETUP_EXECUTABLE) run -config $env(MDD_SETUP_CONFIG)
} else {
  spawn -noecho $env(MDD_SETUP_EXECUTABLE) config setup -config $env(MDD_SETUP_CONFIG)
}
expect {
  -exact "Continue this setup?" {}
  timeout {exit 10}
  eof {exit 11}
}
send "yes\r"
expect "1 Web login"
send "1\r"
expect "Core HTTPS URL"
send "$env(MDD_SETUP_URL)\r"
expect "Trusted leaf SHA-256"
send "$env(MDD_SETUP_PIN)\r"
expect "Web username"
send "setup-user\r"
expect "Web password"
send "correct horse battery staple\r"
if {$env(MDD_SETUP_CANCEL) eq "true"} {
  expect_user "interrupt"
  send "\003"
  expect eof
  exit [lindex [wait] 3]
}
expect {
  -exact "Saved " {}
  timeout {exit 12}
  eof {exit 13}
}
expect eof
exit [lindex [wait] 3]
`)
			process.Env = append(os.Environ(), "MDD_SETUP_EXECUTABLE="+executable, "MDD_SETUP_CONFIG="+cliPath,
				"MDD_SETUP_URL="+server.URL, "MDD_SETUP_PIN="+pin, "MDD_SETUP_PHASE="+interruption, fmt.Sprintf("MDD_SETUP_CANCEL=%t", interrupted))
			var output bytes.Buffer
			process.Stdout, process.Stderr = &output, &output
			stdin, err := process.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			blockCreate.Store(interruption == "create")
			blockLogout.Store(interruption == "logout")
			if err := process.Start(); err != nil {
				t.Fatal(err)
			}
			if interrupted {
				entered := createEntered
				if interruption == "logout" {
					entered = logoutEntered
				}
				select {
				case <-entered:
					_, _ = fmt.Fprintln(stdin, "interrupt")
					if interruption == "logout" {
						// Keep the independent cleanup response in flight while the
						// PTY delivers SIGINT to the CLI, not to this test process.
						time.Sleep(100 * time.Millisecond)
						close(releaseLogout)
					}
				case <-time.After(15 * time.Second):
					_ = process.Process.Kill()
					_ = process.Wait()
					t.Fatalf("CLI_CANCEL_DID_NOT_REACH_CREATE: %s", output.String())
				}
			}
			err = process.Wait()
			_ = stdin.Close()
			blockCreate.Store(false)
			blockLogout.Store(false)
			if interrupted {
				if err == nil {
					t.Fatal("CLI_CANCEL_REPORTED_SUCCESS")
				}
				if _, found := manager.Session(session.Load().(string)); found {
					t.Fatal("CLI_CANCEL_SESSION_LEAK")
				}
				if interruption == "logout" {
					if _, err := loadConfig(cliPath); err != nil {
						t.Fatal("CLI_CANCEL_LOST_SAVED_CONFIG")
					}
					if !strings.Contains(output.String(), "Agent was not started") || strings.Contains(output.String(), "Starting Agent using") {
						t.Fatalf("CLI_CANCEL_STARTED_RUNTIME: %s", output.String())
					}
					continue
				}
				if _, err := os.Stat(cliPath + ".setup-pending"); err != nil {
					t.Fatal("CLI_CANCEL_LOST_PENDING")
				}
				if _, err := os.Stat(cliPath); !os.IsNotExist(err) {
					t.Fatal("CLI_CANCEL_PUBLISHED_CONFIG")
				}
				continue
			}
			if err != nil {
				t.Fatalf("CLI_SETUP: %v\n%s", err, output.String())
			}
			if _, err := loadConfig(cliPath); err != nil {
				t.Fatalf("CLI_SETUP_CONFIG: %v", err)
			}
		}
	}
}
