package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
)

func TestManagedConnectionTransaction(t *testing.T) {
	for _, scenario := range []string{"save", "bad-pin", "bad-token", "new-target-retained-token", "new-target-new-token", "stale", "session", "revoke-before-commit", "cancel-before-commit", "lost-response", "pin-writer", "unknown-field", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			settings := testConfig(t)
			authPath := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(authPath, []byte(`{"version":1,"username":"setup-user","salt":"00112233445566778899aabbccddeeff","password_hash":"ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302","agent_token":"0123456789abcdef0123456789abcdef"}`), 0600); err != nil {
				t.Fatal(err)
			}
			auth, err := adminauth.NewManager(authPath, true, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			settings.Agent.ServerToken, err = auth.CreateAgentToken(settings.Agent.ID, strings.Repeat("a", 32), nil)
			if err != nil {
				t.Fatal(err)
			}
			authHandler, err := adminauth.NewHandler(auth)
			if err != nil {
				t.Fatal(err)
			}
			var checks atomic.Int32
			var revoked atomic.Bool
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			path := filepath.Join(t.TempDir(), "config.json")
			settings.configPath = path
			settings.Agent.PINs = map[string]string{"89010000000000000001": "2468"}
			settings.Agent.PINRevisions = map[string]string{"89010000000000000001": strings.Repeat("c", 32)}
			pinStore, _ := newAgentPINCredentials(path, nil)
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/auth/agent-credentials/check" {
					checks.Add(1)
					switch scenario {
					case "revoke-before-commit":
						revoked.Store(true)
					case "cancel-before-commit":
						cancel()
					case "pin-writer":
						if _, err := pinStore.Save(t.Context(), "89010000000000000001", "1234", strings.Repeat("c", 32)); err == nil {
							t.Error("PIN writer entered another owner's transaction")
						}
					}
				}
				authHandler.ServeHTTP(w, r)
			}))
			defer peer.Close()
			fingerprint := sha256.Sum256(peer.Certificate().Raw)
			settings.Agent.ServerURL = strings.Replace(peer.URL, "https:", "wss:", 1) + "/v1/agent/ws"
			settings.Agent.TLSMode, settings.Agent.TLSFingerprint = "pin", hex.EncodeToString(fingerprint[:])
			if err := saveConfig(path, settings); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			handler := &localManagementHandler{settings: settings, session: strings.Repeat("b", 32), slots: make(chan struct{}, 4), authorize: func() error {
				if revoked.Load() {
					return errors.New("revoked")
				}
				return nil
			}}
			get := func() (localManagementState, []byte) {
				r := httptest.NewRecorder()
				handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/v1/connection", nil))
				if r.Code != http.StatusOK {
					t.Fatalf("read state: %d", r.Code)
				}
				var state localManagementState
				if err := json.Unmarshal(r.Body.Bytes(), &state); err != nil {
					t.Fatal(err)
				}
				return state, r.Body.Bytes()
			}
			initial, payload := get()
			for _, secret := range []string{settings.Agent.ServerToken, settings.Control.Token, "2468", "pins", "control"} {
				if secret != "" && bytes.Contains(payload, []byte(secret)) {
					t.Fatal("management response included a secret field")
				}
			}
			if !initial.Loaded || initial.Running.AgentID != settings.Agent.ID {
				t.Fatal("initial running state mismatch")
			}
			change := localManagementEdit{Version: 1, Session: initial.Session, Revision: initial.Revision, Change: connectionChange{Server: settings.Agent.ServerURL, TLSMode: "pin"}}
			wantSuccess := scenario == "save" || scenario == "lost-response" || scenario == "pin-writer" || scenario == "new-target-new-token"
			var otherRequests atomic.Int32
			switch scenario {
			case "save", "lost-response", "pin-writer":
				change.Change.Token, err = auth.IssueAgentToken(settings.Agent.ID)
				if err != nil {
					t.Fatal(err)
				}
			case "bad-pin":
				change.Change.Fingerprint = strings.Repeat("0", 64)
				change.Change.Token = settings.Agent.ServerToken
			case "bad-token":
				change.Change.Token = strings.Repeat("wrong", 10)
			case "stale":
				change.Revision = strings.Repeat("0", 64)
			case "session":
				change.Session = strings.Repeat("0", 32)
			case "new-target-retained-token", "new-target-new-token":
				other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherRequests.Add(1); authHandler.ServeHTTP(w, r) }))
				defer other.Close()
				pin := sha256.Sum256(other.Certificate().Raw)
				change.Change.Server, change.Change.Fingerprint = other.URL, hex.EncodeToString(pin[:])
				if scenario == "new-target-new-token" {
					change.Change.Token = settings.Agent.ServerToken
				}
			}
			body, _ := json.Marshal(change)
			if scenario == "unknown-field" {
				body = append(body[:len(body)-1], []byte(`,"path":"other-config"}`)...)
			}
			if scenario == "oversize" {
				body = []byte(strings.Repeat(" ", 8193) + string(body))
			}
			request := httptest.NewRequest(http.MethodPut, "/v1/connection", bytes.NewReader(body)).WithContext(ctx)
			response := httptest.NewRecorder()
			if scenario == "lost-response" {
				handler.ServeHTTP(&discardManagementResponse{header: http.Header{}}, request)
			} else {
				handler.ServeHTTP(response, request)
			}
			after, _ := os.ReadFile(path)
			if !wantSuccess {
				if scenario == "new-target-retained-token" && otherRequests.Load() != 0 {
					t.Fatal("RETAINED_TOKEN_TARGET: contacted a changed recipient without a new credential")
				}
				if response.Code == http.StatusOK || !bytes.Equal(before, after) {
					t.Fatalf("rejected change modified config: %d", response.Code)
				}
				return
			}
			if scenario != "lost-response" && response.Code != http.StatusOK {
				t.Fatalf("save: %d %s", response.Code, response.Body.String())
			}
			stored, err := readConfigForEdit(path, false)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Agent.ID != settings.Agent.ID || stored.Control != settings.Control || !reflect.DeepEqual(stored.Agent.PINs, settings.Agent.PINs) || stored.Agent.ModemEnabled != settings.Agent.ModemEnabled {
				t.Fatal("non-connection fields changed")
			}
			fresh, _ := get()
			if fresh.Loaded || fresh.Revision == initial.Revision || fresh.Running != initial.Running {
				t.Fatal("saved settings were labeled loaded or the original process changed")
			}
			if scenario == "pin-writer" {
				if _, err := pinStore.Save(t.Context(), "89010000000000000001", "1234", strings.Repeat("c", 32)); err != nil {
					t.Fatal(err)
				}
				latest, err := readConfigForEdit(path, false)
				if err != nil || latest.Agent.ServerURL != stored.Agent.ServerURL || latest.Agent.PINs["89010000000000000001"] != "1234" {
					t.Fatal("PIN transaction overwrote connection fields")
				}
			}
			if scenario != "new-target-new-token" && checks.Load() != 1 {
				t.Fatalf("unexpected verification count %d", checks.Load())
			}
		})
	}
}

type discardManagementResponse struct{ header http.Header }

func (w *discardManagementResponse) Header() http.Header     { return w.header }
func (*discardManagementResponse) WriteHeader(int)           {}
func (*discardManagementResponse) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestPINMutationOwnsFileBeforeReading(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "save", true: "remove"}[remove], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := saveConfig(path, testConfig(t)); err != nil {
				t.Fatal(err)
			}
			lock, err := lockConfigMutation(path)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			// The current owner is between writes. A competing writer must reject
			// before observing that owner's intermediate contents.
			if err := os.WriteFile(path, []byte("temporary-incomplete-config"), 0600); err != nil {
				t.Fatal(err)
			}
			store, _ := newAgentPINCredentials(path, nil)
			if remove {
				_, err = store.Remove(t.Context(), "89010000000000000001", "pin-old")
			} else {
				_, err = store.Save(t.Context(), "89010000000000000001", "1234", "")
			}
			if err == nil || !strings.Contains(err.Error(), "another configuration setup") {
				t.Fatalf("PIN_READ_BEFORE_LOCK: got %v", err)
			}
		})
	}
}
