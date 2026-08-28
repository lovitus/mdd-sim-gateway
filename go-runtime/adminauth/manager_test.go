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

func testManager(t *testing.T, secure bool, now func() time.Time) *Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	payload := `{"version":1,"username":"fanli","salt":"00112233445566778899aabbccddeeff","password_hash":"` + testHash + `","agent_token":"ignored-compatible-field"}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(path, secure, now)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
