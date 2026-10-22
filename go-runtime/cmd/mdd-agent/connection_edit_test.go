package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
)

func TestConnectionEditPreservesOwnerAndChecksCredential(t *testing.T) {
	for _, scenario := range []string{"retain", "new-endpoint", "bad-pin", "system-trust", "invalid-token", "cancelled", "external-before", "external-during", "replaced-same-bytes", "pending-setup", "locked"} {
		t.Run(scenario, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			credential := `{"version":1,"username":"setup-user","salt":"00112233445566778899aabbccddeeff","password_hash":"ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302","agent_token":"0123456789abcdef0123456789abcdef"}`
			if err := os.WriteFile(authPath, []byte(credential), 0600); err != nil {
				t.Fatal(err)
			}
			manager, err := adminauth.NewManager(authPath, true, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := newEditableConfig()
			if err != nil {
				t.Fatal(err)
			}
			settings.Agent.ServerToken, err = manager.CreateAgentToken(settings.Agent.ID, strings.Repeat("a", 32), nil)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := adminauth.NewHandler(manager)
			if err != nil {
				t.Fatal(err)
			}
			var checks, unexpected atomic.Int32
			var beforeCheck atomic.Value
			beforeCheck.Store(func() {})
			endpoint := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/auth/status":
				case "/api/auth/agent-credentials/check":
					checks.Add(1)
					beforeCheck.Load().(func())()
				default:
					unexpected.Add(1)
					http.Error(w, "unexpected operation", http.StatusForbidden)
					return
				}
				handler.ServeHTTP(w, r)
			})
			server := httptest.NewTLSServer(endpoint)
			defer server.Close()
			pin := sha256.Sum256(server.Certificate().Raw)
			settings.Agent.ServerURL = strings.Replace(server.URL, "https:", "wss:", 1) + "/v1/agent/ws"
			settings.Agent.TLSMode, settings.Agent.TLSFingerprint = "pin", hex.EncodeToString(pin[:])
			settings.Agent.PINs = map[string]string{"8900000000000000001": "1234"}
			settings.Agent.PINRevisions = map[string]string{"8900000000000000001": strings.Repeat("a", 32)}
			settings.Agent.ModemEnabled, settings.Agent.ModemSIMAPDU = true, true
			settings.Agent.ModemProfiles = json.RawMessage(`{"retained":"opaque-existing-profile"}`)
			settings.Agent.AdvertiseHost = "display-only.example"
			settings.Agent.RawUSBSource = runtime.GOOS == "windows" || runtime.GOOS == "linux"
			settings.Agent.RawUSBImporter = runtime.GOOS == "linux"
			// Legacy zero defaults must not be silently rewritten by validation.
			settings.ScanIntervalMS, settings.RetryBaseMS, settings.RetryCapMS, settings.OperationTimeoutSeconds = 0, 0, 0, 0
			path := filepath.Join(t.TempDir(), "config.json")
			if err := saveConfig(path, settings); err != nil {
				t.Fatal(err)
			}
			edit, err := readConnectionEdit(path)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			authBefore, err := os.ReadFile(authPath)
			if err != nil {
				t.Fatal(err)
			}
			change := connectionChange{Server: settings.Agent.ServerURL, TLSMode: "pin"}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantSuccess, wantChecks := false, int32(0)
			retained := original
			switch scenario {
			case "retain":
				wantSuccess, wantChecks = true, 1
			case "new-endpoint":
				other := httptest.NewTLSServer(endpoint)
				defer other.Close()
				digest := sha256.Sum256(other.Certificate().Raw)
				change.Server, change.Fingerprint = other.URL, hex.EncodeToString(digest[:])
				wantSuccess, wantChecks = true, 1
			case "bad-pin":
				change.Fingerprint = strings.Repeat("0", 64)
			case "system-trust":
				change.TLSMode = "system"
			case "invalid-token":
				change.Token, wantChecks = strings.Repeat("wrong-token", 4), 1
			case "cancelled":
				cancel()
			case "external-before", "external-during":
				replacement := settings
				replacement.Agent.ID = "external-owner"
				retained, err = json.MarshalIndent(replacement, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				write := func() {
					if err := os.WriteFile(path, retained, 0600); err != nil {
						t.Error(err)
					}
				}
				if scenario == "external-before" {
					write()
				} else {
					beforeCheck.Store(write)
					wantChecks = 1
				}
			case "replaced-same-bytes":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			case "pending-setup":
				if err := os.WriteFile(path+".setup-pending", []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "locked":
				lock, err := lockConfigMutation(path)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			err = edit.save(ctx, change)
			if (err == nil) != wantSuccess {
				t.Fatalf("save success=%t, want %t: %v", err == nil, wantSuccess, err)
			}
			if checks.Load() != wantChecks || unexpected.Load() != 0 {
				t.Fatalf("credential checks=%d want=%d; non-check requests=%d", checks.Load(), wantChecks, unexpected.Load())
			}
			actual, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !wantSuccess {
				if !bytes.Equal(actual, retained) {
					t.Fatal("failed edit overwrote the retained configuration")
				}
			} else {
				var got config
				if err := strictSetupJSON(actual, &got); err != nil {
					t.Fatal(err)
				}
				want := edit.settings
				want.Agent.ServerURL, err = normalizeSetupServer(change.Server)
				if err != nil {
					t.Fatal(err)
				}
				if change.Fingerprint != "" {
					want.Agent.TLSFingerprint = change.Fingerprint
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("connection edit changed identity, local control, PINs, device intent or legacy defaults")
				}
				if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
					t.Fatal("configuration is not private", err)
				}
			}
			authAfter, err := os.ReadFile(authPath)
			if err != nil || !bytes.Equal(authBefore, authAfter) {
				t.Fatal("connection verification modified Core credentials", err)
			}
		})
	}
}

func TestSetupExplicitTrustDoesNotReuseHiddenPin(t *testing.T) {
	for _, mode := range []string{"system", "pin", ""} {
		t.Run(mode, func(t *testing.T) {
			tx, err := beginSetup(filepath.Join(t.TempDir(), "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer tx.close()
			tx.pending.Settings.Agent.TLSFingerprint = strings.Repeat("a", 64)
			tx.pending.Settings.Agent.TLSMode = "pin"
			err = tx.configure(setupInput{Server: "https://gateway.example", TLSMode: mode})
			if mode == "pin" {
				if err == nil {
					t.Fatal("explicit pin mode accepted a hidden old fingerprint")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "system" && (tx.pending.Settings.Agent.TLSMode != "system" || tx.pending.Settings.Agent.TLSFingerprint != "") {
				t.Fatal("system verification retained hidden pin")
			}
			if mode == "" && tx.pending.Settings.Agent.TLSFingerprint != strings.Repeat("a", 64) {
				t.Fatal("legacy CLI empty trust changed the retained pin")
			}
		})
	}
}
