package adminauth

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
)

const (
	testPassword = "correct horse battery staple"
	testHash     = "ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302"
)

func TestExistingPythonScryptCredentialAndSessionContract(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	manager := testManager(t, false, func() time.Time { return now })
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := manager.Login("fanli", "wrong", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt=%d err=%v", attempt, err)
		}
	}
	_, err := manager.Login("fanli", testPassword, "127.0.0.1")
	var throttle *ThrottleError
	if !errors.As(err, &throttle) || throttle.RetryAfter != 60 {
		t.Fatalf("throttle=%+v err=%v", throttle, err)
	}
	now = now.Add(61 * time.Second)
	login, err := manager.Login("fanli", testPassword, "127.0.0.1")
	if err != nil || len(login.Token) < 32 || login.Session.CSRF == "" || login.Session.Subject == "" {
		t.Fatalf("login=%+v err=%v", login, err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/action", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	request.Header.Set("X-MDD-CSRF-Token", login.Session.CSRF)
	if _, err := manager.Authorize(request, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.VerifyBrowserSession(context.Background(), request); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("header-only browser session err=%v", err)
	}
	request.AddCookie(&http.Cookie{Name: SessionCookie, Value: login.Token})
	if subject, err := manager.VerifyBrowserSession(context.Background(), request); err != nil || subject != login.Session.Subject {
		t.Fatalf("subject=%q err=%v", subject, err)
	}
	now = now.Add(SessionTTL + time.Second)
	if _, found := manager.Session(login.Token); found {
		t.Fatal("expired session remained valid")
	}
}

func TestAuthHTTPLoginStatusAndLogout(t *testing.T) {
	manager := testManager(t, false, time.Now)
	handler, err := NewHandler(manager)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	loginRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/auth/login", strings.NewReader(`{"username":"fanli","password":"`+testPassword+`"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse, err := http.DefaultClient.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusOK || len(loginResponse.Cookies()) != 1 {
		t.Fatalf("login status=%d cookies=%v", loginResponse.StatusCode, loginResponse.Cookies())
	}
	cookie := loginResponse.Cookies()[0]
	if cookie.Name != SessionCookie || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != int(SessionTTL.Seconds()) {
		t.Fatalf("cookie=%+v", cookie)
	}
	statusRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/auth/status", nil)
	statusRequest.AddCookie(cookie)
	statusResponse, err := http.DefaultClient.Do(statusRequest)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.NewDecoder(statusResponse.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	_ = statusResponse.Body.Close()
	if status["authenticated"] != true || status["username"] != "fanli" || status["token"] == "" || status["csrf"] == "" {
		t.Fatalf("status=%v", status)
	}
	logoutRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/auth/logout", nil)
	logoutRequest.AddCookie(cookie)
	logoutSession, found := manager.Session(cookie.Value)
	if !found {
		t.Fatal("session disappeared before logout")
	}
	logoutRequest.Header.Set("X-MDD-CSRF-Token", logoutSession.CSRF)
	logoutResponse, err := http.DefaultClient.Do(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = logoutResponse.Body.Close()
	if logoutResponse.StatusCode != http.StatusOK || logoutResponse.Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout status=%d cookies=%v", logoutResponse.StatusCode, logoutResponse.Cookies())
	}
	if _, found := manager.Session(cookie.Value); found {
		t.Fatal("logout did not revoke session")
	}
}

func TestChangePasswordAtomicallyReplacesCredentialAndRevokesSessions(t *testing.T) {
	manager := testManager(t, false, time.Now)
	login, err := manager.Login("fanli", testPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ChangePassword(testPassword, "new secure password"); err != nil {
		t.Fatal(err)
	}
	if _, found := manager.Session(login.Token); found {
		t.Fatal("password change left old session valid")
	}
	if _, err := manager.Login("fanli", testPassword, "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password err=%v", err)
	}
	if _, err := manager.Login("fanli", "new secure password", "127.0.0.1"); err != nil {
		t.Fatalf("new password login: %v", err)
	}
	path := manager.credentialPath
	reloaded, err := NewManager(path, false, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Login("fanli", "new secure password", "127.0.0.1"); err != nil {
		t.Fatalf("reloaded manager login: %v", err)
	}
}

func TestAuthHTTPPasswordChangeRequiresCSRFAndRevokesCookie(t *testing.T) {
	manager := testManager(t, false, time.Now)
	handler, err := NewHandler(manager)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	loginRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/auth/login", strings.NewReader(`{"username":"fanli","password":"`+testPassword+`"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse, err := http.DefaultClient.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	cookie := loginResponse.Cookies()[0]
	_ = loginResponse.Body.Close()
	session, found := manager.Session(cookie.Value)
	if !found {
		t.Fatal("login session missing")
	}
	requestBody := `{"current_password":"` + testPassword + `","new_password":"new secure password"}`
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/auth/password", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	request.Header.Set("X-MDD-CSRF-Token", session.CSRF)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Cookies()[0].MaxAge != -1 {
		t.Fatalf("password status=%d cookies=%v", response.StatusCode, response.Cookies())
	}
	_ = response.Body.Close()
	status, _ := http.NewRequest(http.MethodGet, server.URL+"/api/auth/status", nil)
	status.AddCookie(cookie)
	statusResponse, err := http.DefaultClient.Do(status)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.NewDecoder(statusResponse.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	_ = statusResponse.Body.Close()
	if state["authenticated"] == true {
		t.Fatal("revoked password-change cookie remained authenticated")
	}
}

func TestCredentialFileRejectsTrailingOrIncompleteData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"version":1} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(path, true, time.Now); err == nil {
		t.Fatal("trailing credential data was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"username":"fanli"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(path, true, time.Now); err == nil {
		t.Fatal("incomplete credential data was accepted")
	}
}

func TestScopedAgentCredentialsMigrateRotateRevokeAndPersist(t *testing.T) {
	manager := testManager(t, false, time.Now)
	legacy, err := manager.TokenForAgent(context.Background(), "agent-b")
	if err != nil || len(legacy) < 32 || manager.AgentCredentials().Mode != AgentCredentialTransition {
		t.Fatalf("legacy=%q err=%v status=%+v", legacy, err, manager.AgentCredentials())
	}
	first, err := manager.IssueAgentToken("agent-a")
	if err != nil || len(first) < 32 || first == legacy {
		t.Fatalf("first token length=%d err=%v", len(first), err)
	}
	resolved, err := manager.TokenForAgent(context.Background(), "agent-a")
	if err != nil || resolved != first {
		t.Fatalf("resolved issued token err=%v", err)
	}
	second, err := manager.IssueAgentToken("agent-a")
	if err != nil || second == first {
		t.Fatal("Agent token rotation did not replace the scoped credential")
	}
	if err := manager.RevokeAgentToken("agent-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.TokenForAgent(context.Background(), "agent-a"); !errors.Is(err, ErrAgentCredential) {
		t.Fatalf("revoked Agent err=%v", err)
	}
	if err := manager.UnenrollAgentToken("agent-a"); err != nil {
		t.Fatal(err)
	}
	if fallback, err := manager.TokenForAgent(context.Background(), "agent-a"); err != nil || fallback != legacy {
		t.Fatalf("unenrolled fallback=%q err=%v", fallback, err)
	}
	if _, err := manager.IssueAgentToken("agent-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RevokeAgentToken("agent-a"); err != nil {
		t.Fatal(err)
	}
	if changed, err := manager.SetAgentCredentialMode(AgentCredentialScoped); err != nil || !changed {
		t.Fatalf("scoped changed=%v err=%v", changed, err)
	}
	if _, err := manager.TokenForAgent(context.Background(), "unknown-agent"); !errors.Is(err, ErrAgentCredential) {
		t.Fatalf("unknown scoped Agent err=%v", err)
	}
	if err := manager.UnenrollAgentToken("agent-a"); !errors.Is(err, ErrAgentCredential) {
		t.Fatalf("scoped mode allowed identity deletion: %v", err)
	}
	if err := manager.ChangePassword(testPassword, "new secure password"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(manager.credentialPath, false, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	status := reloaded.AgentCredentials()
	if status.Mode != AgentCredentialScoped || status.LegacyFallbackEnabled || len(status.Active) != 0 ||
		len(status.Revoked) != 1 || status.Revoked[0] != "agent-a" {
		t.Fatalf("reloaded status=%+v", status)
	}
	if _, err := reloaded.Login("fanli", "new secure password", "127.0.0.1"); err != nil {
		t.Fatalf("password change did not preserve a usable administrator credential: %v", err)
	}
}

func TestAgentCredentialHTTPReturnsSecretOnceAndInvalidatesConnections(t *testing.T) {
	manager := testManager(t, false, time.Now)
	var invalidated []string
	handler, err := NewHandler(manager, WithAgentCredentialInvalidator(func(agentID string) {
		invalidated = append(invalidated, agentID)
	}))
	if err != nil {
		t.Fatal(err)
	}
	login, err := manager.Login("fanli", testPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, payload string) *httptest.ResponseRecorder {
		input := httptest.NewRequest(method, "/api/auth/agent-credentials", strings.NewReader(payload))
		input.AddCookie(&http.Cookie{Name: SessionCookie, Value: login.Token})
		if method == http.MethodPost {
			input.Header.Set("Content-Type", "application/json")
			input.Header.Set("X-MDD-CSRF-Token", login.Session.CSRF)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, input)
		return response
	}
	issued := request(http.MethodPost, `{"action":"issue","agent_id":"agent-a"}`)
	var issueResult struct {
		AgentToken string `json:"agent_token"`
	}
	if issued.Code != http.StatusOK || json.Unmarshal(issued.Body.Bytes(), &issueResult) != nil || len(issueResult.AgentToken) < 32 {
		t.Fatalf("issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	listed := request(http.MethodGet, "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), issueResult.AgentToken) ||
		!strings.Contains(listed.Body.String(), `"active":["agent-a"]`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	mode := request(http.MethodPost, `{"action":"set_mode","mode":"scoped"}`)
	revoked := request(http.MethodPost, `{"action":"revoke","agent_id":"agent-a"}`)
	if mode.Code != http.StatusOK || revoked.Code != http.StatusOK || len(invalidated) != 3 ||
		invalidated[0] != "agent-a" || invalidated[1] != "" || invalidated[2] != "agent-a" {
		t.Fatalf("mode=%d revoke=%d invalidated=%v", mode.Code, revoked.Code, invalidated)
	}
	legacy := httptest.NewRecorder()
	handler.ServeHTTP(legacy, httptest.NewRequest(http.MethodPost, "/api/auth/agent-token", strings.NewReader(`{}`)))
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy global Agent token route status=%d", legacy.Code)
	}
}

func testManager(t *testing.T, secure bool, now func() time.Time) *Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	payload := `{"version":1,"username":"fanli","salt":"00112233445566778899aabbccddeeff","password_hash":"` + testHash + `","agent_token":"0123456789abcdef0123456789abcdef"}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(path, secure, now)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
