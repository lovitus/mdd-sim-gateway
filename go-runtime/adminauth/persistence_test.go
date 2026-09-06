package adminauth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const persistenceTestToken = "0123456789abcdef0123456789abcdef"

func validCredentialPayload() []byte {
	return []byte(`{"version":1,"username":"fanli","salt":"00112233445566778899aabbccddeeff","password_hash":"` +
		testHash + `","agent_token":"` + persistenceTestToken + `","agent_token_mode":"scoped","agent_tokens":{"agent-a":"` + persistenceTestToken + `"}}` + "\n")
}

func TestCredentialPersistenceHandlerAtomicallyReplacesExactRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := NewCredentialPersistenceHandler(path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, CredentialPersistencePath, strings.NewReader(string(validCredentialPayload())))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	payload, err := os.ReadFile(path)
	if err != nil || string(payload) != string(validCredentialPayload()) {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
}

func TestCredentialPersistenceRejectsMalformedDocumentAndSymlinkTarget(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "auth.json")
	handler, err := NewCredentialPersistenceHandler(path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, CredentialPersistencePath, strings.NewReader(`{"version":1}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d", response.Code)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest(http.MethodPost, CredentialPersistencePath, strings.NewReader(string(validCredentialPayload())))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("symlink status=%d", response.Code)
	}
	if payload, err := os.ReadFile(target); err != nil || string(payload) != "untouched" {
		t.Fatalf("target=%q err=%v", payload, err)
	}
}

func TestRemoteCredentialPersisterUsesAuthenticatedUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket helper is not used on Windows")
	}
	directory := t.TempDir()
	socketPath := filepath.Join(os.TempDir(), "mdd-auth-persistence-test.sock")
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "auth.json")
	handler, err := NewCredentialPersistenceHandler(path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get(localTokenHeader) != persistenceTestToken {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(response, request)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	persister, err := NewRemoteCredentialPersister(socketPath, persistenceTestToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := persister.PersistCredential(validCredentialPayload()); err != nil {
		t.Fatal(err)
	}
	if payload, err := os.ReadFile(path); err != nil || string(payload) != string(validCredentialPayload()) {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	_ = server.Shutdown(context.Background())
	if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

func TestRemoteCredentialPersisterConfirmsCommitAfterLostPostResponse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket helper is not used on Windows")
	}
	directory := t.TempDir()
	socketPath := filepath.Join(os.TempDir(), "mdd-auth-persistence-uncertain.sock")
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "auth.json")
	handler, err := NewCredentialPersistenceHandler(path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get(localTokenHeader) != persistenceTestToken {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodPost {
			payload, readErr := io.ReadAll(io.LimitReader(request.Body, maxAuthBytes+1))
			if readErr != nil || persistCredentialFile(path, payload, os.Getuid(), os.Getgid()) != nil {
				http.Error(response, "failed", http.StatusInternalServerError)
				return
			}
			connection, _, hijackErr := response.(http.Hijacker).Hijack()
			if hijackErr == nil {
				_ = connection.Close()
			}
			return
		}
		handler.ServeHTTP(response, request)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	persister, err := NewRemoteCredentialPersister(socketPath, persistenceTestToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := persister.PersistCredential(validCredentialPayload()); err != nil {
		t.Fatalf("lost committed response was not resolved by exact digest: %v", err)
	}
	_ = server.Shutdown(context.Background())
	if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}
