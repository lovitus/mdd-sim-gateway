package adminauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func enrollmentRequest(t *testing.T, handler http.Handler, login LoginResult, action, id, requestID string) (int, string) {
	t.Helper()
	input := map[string]string{"action": action, "agent_id": id}
	if requestID != "" {
		input["enrollment_id"] = requestID
	}
	payload, _ := json.Marshal(input)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/agent-credentials", bytes.NewReader(payload))
	req.Header.Set("X-MDD-Session", login.Token)
	req.Header.Set("X-MDD-CSRF-Token", login.Session.CSRF)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	var result struct {
		Token string `json:"agent_token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return res.Code, result.Token
}

func TestAgentSetupHTTPDelivery(t *testing.T) {
	manager := testManager(t, true, time.Now)
	existing, err := manager.IssueAgentToken("existing-agent")
	if err != nil {
		t.Fatal(err)
	}
	login, err := manager.Login("fanli", testPassword, "setup")
	if err != nil {
		t.Fatal(err)
	}
	var invalidations atomic.Int32
	handler, _ := NewHandler(manager, WithAgentCredentialInvalidator(func(string) { invalidations.Add(1) }))
	requestID := strings.Repeat("a", 32)
	code, original := enrollmentRequest(t, handler, login, "create", "new-agent", requestID)
	if code != http.StatusOK || len(original) < 32 {
		t.Fatalf("CREATE_ONLY: status=%d; expected a new independent credential", code)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			code, token := enrollmentRequest(t, handler, login, "create", "new-agent", requestID)
			if code != http.StatusOK || token != original {
				t.Errorf("REPLAY_IDENTITY: status=%d token changed=%t", code, token != original)
			}
		})
	}
	group.Wait()
	for _, row := range [][2]string{{"existing-agent", requestID}, {"new-agent", strings.Repeat("b", 32)}, {"different-agent", requestID}} {
		if code, _ := enrollmentRequest(t, handler, login, "create", row[0], row[1]); code != http.StatusConflict {
			t.Fatalf("CONFLICT: %v status=%d", row, code)
		}
	}
	if token, _ := manager.TokenForAgent(context.Background(), "existing-agent"); token != existing || invalidations.Load() != 0 {
		t.Fatal("EXISTING_AGENT_CHANGED")
	}
	for _, action := range []string{"issue", "revoke", "unenroll"} {
		id, request := "retire-"+action, strings.Repeat(action, 12)
		if code, _ := enrollmentRequest(t, handler, login, "create", id, request); code != http.StatusOK {
			t.Fatal(code)
		}
		if code, _ := enrollmentRequest(t, handler, login, action, id, ""); code != http.StatusOK {
			t.Fatal(code)
		}
		if code, _ := enrollmentRequest(t, handler, login, "create", id, request); code != http.StatusConflict {
			t.Fatalf("RETIRED_ENROLLMENT: %s status=%d", action, code)
		}
	}
	if err := manager.ChangePassword(testPassword, "setup changed password"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(manager.credentialPath, true, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	login, err = reloaded.Login("fanli", "setup changed password", "after-restart")
	if err != nil {
		t.Fatal(err)
	}
	handler, _ = NewHandler(reloaded)
	if code, token := enrollmentRequest(t, handler, login, "create", "new-agent", requestID); code != http.StatusOK || token != original {
		t.Fatal("RESTART_REPLAY_CHANGED")
	}
	for _, action := range []string{"issue", "revoke", "unenroll"} {
		if code, _ := enrollmentRequest(t, handler, login, "create", "retire-"+action, strings.Repeat(action, 12)); code != http.StatusConflict {
			t.Fatal("RETIRED_METADATA_LOST")
		}
	}
	login.Session.CSRF = "wrong"
	if code, _ := enrollmentRequest(t, handler, login, "create", "new-agent", requestID); code != http.StatusForbidden {
		t.Fatal("RECOVERY_BYPASSED_CSRF")
	}
}

func TestAgentSetupLostHelperReceipt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("privileged Unix credential helper is not used on Windows")
	}
	manager := testManager(t, true, time.Now)
	path := manager.credentialPath
	helper, err := NewCredentialPersistenceHandler(path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	var lose atomic.Bool
	var lost atomic.Bool
	var published atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			receipt := httptest.NewRecorder()
			helper.ServeHTTP(receipt, r)
			if receipt.Code == http.StatusOK {
				published.Store(append([]byte(nil), body...))
			}
			if lose.Load() {
				lost.Store(true)
				http.Error(w, "lost receipt", 503)
				return
			}
			w.WriteHeader(receipt.Code)
			_, _ = w.Write(receipt.Body.Bytes())
			return
		}
		if lose.Load() && lost.Load() {
			http.Error(w, "digest unavailable", 503)
			return
		}
		helper.ServeHTTP(w, r)
	}))
	defer server.Close()
	// Only the IPC transport is redirected to a real loopback helper. Manager,
	// handler, serialization, compare-and-publish and digest readback are real.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	base, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, base.URL.Host)
	}
	persister := &RemoteCredentialPersister{token: persistenceTestToken, http: &http.Client{Transport: transport, Timeout: time.Second}}
	manager, err = NewManager(path, true, time.Now, WithCredentialPersister(persister))
	if err != nil {
		t.Fatal(err)
	}
	login, _ := manager.Login("fanli", testPassword, "lost")
	handler, _ := NewHandler(manager)
	lose.Store(true)
	request := strings.Repeat("z", 32)
	if code, _ := enrollmentRequest(t, handler, login, "create", "new-agent", request); code != http.StatusServiceUnavailable {
		t.Fatalf("LOST_RECEIPT: status=%d", code)
	}
	onDisk, _ := os.ReadFile(path)
	if committed, ok := published.Load().([]byte); !ok || !bytes.Equal(onDisk, committed) {
		t.Fatal("helper did not publish before losing receipt")
	}
	if code, _ := enrollmentRequest(t, handler, login, "issue", "existing-agent", ""); code < 400 {
		t.Fatal("UNCONFIRMED_WRITE_OVERWRITTEN")
	}
	lose.Store(false)
	code, token := enrollmentRequest(t, handler, login, "create", "new-agent", request)
	if code != http.StatusOK || len(token) < 32 {
		t.Fatalf("RECOVER_ORIGINAL: status=%d", code)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(onDisk, after) {
		t.Fatal("RECOVERY_GENERATED_SECOND_TOKEN")
	}
	if code, replay := enrollmentRequest(t, handler, login, "create", "new-agent", request); code != http.StatusOK || replay != token {
		t.Fatal("replay changed credential")
	}
	for _, mutation := range []struct {
		name  string
		apply func() error
	}{
		{"rotate", func() error { _, err := manager.IssueAgentToken("existing-agent"); return err }},
		{"password", func() error { return manager.ChangePassword(testPassword, "replacement-password") }},
		{"revoke", func() error { return manager.RevokeAgentToken("existing-agent") }},
		{"mode", func() error { _, err := manager.SetAgentCredentialMode(AgentCredentialScoped); return err }},
	} {
		lost.Store(false)
		lose.Store(true)
		if err := mutation.apply(); err == nil {
			t.Fatalf("%s: unknown receipt was reported successful", mutation.name)
		}
		committed, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if mutation.name == "mode" {
			fresh, err := manager.Login("fanli", "replacement-password", "pending-mode")
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/auth/agent-credentials", strings.NewReader(`{"action":"set_mode","mode":"transition"}`))
			req.Header.Set("X-MDD-Session", fresh.Token)
			req.Header.Set("X-MDD-CSRF-Token", fresh.Session.CSRF)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusServiceUnavailable {
				t.Fatal("PENDING_MODE_REPORTED_NOOP")
			}
		}
		if _, err := manager.IssueAgentToken("unrelated-agent"); err == nil {
			t.Fatal("UNCONFIRMED_MUTATION_OVERWRITTEN")
		}
		lose.Store(false)
		if err := mutation.apply(); err != nil {
			t.Fatalf("EXISTING_MUTATION_RETRY: %s: %v", mutation.name, err)
		}
		confirmed, _ := os.ReadFile(path)
		if !bytes.Equal(committed, confirmed) {
			t.Fatalf("%s: retry changed the original publication", mutation.name)
		}
	}
}
