package agentlink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
)

func TestCredentialHandlerRevokesUpgradedButUnregisteredAgent(t *testing.T) {
	for _, mutation := range []string{
		`{"action":"revoke","agent_id":"agent-1"}`,
		`{"action":"issue","agent_id":"agent-1"}`,
		`{"action":"set_mode","mode":"scoped"}`,
	} {
		t.Run(mutation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			credential := `{"version":1,"username":"admin","salt":"00112233445566778899aabbccddeeff","password_hash":"ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302","agent_token":"` + testToken + `"}`
			if err := os.WriteFile(path, []byte(credential), 0600); err != nil {
				t.Fatal(err)
			}
			auth, err := adminauth.NewManager(path, false, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			login, err := auth.Login("admin", "correct horse battery staple", "peer")
			if err != nil {
				t.Fatal(err)
			}
			server, err := NewServer(auth)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := adminauth.NewHandler(auth, adminauth.WithAgentCredentialInvalidator(server.DisconnectAgent))
			if err != nil {
				t.Fatal(err)
			}
			served := make(chan struct{})
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(served)
				server.ServeHTTP(w, r)
			}))
			defer httpServer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			socket, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1), &websocket.DialOptions{
				HTTPHeader: http.Header{"Authorization": {"Bearer " + testToken}, "X-Mdd-Agent-Id": {"agent-1"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer socket.CloseNow()
			request := httptest.NewRequest(http.MethodPost, "/api/auth/agent-credentials", strings.NewReader(mutation))
			request.Header.Set("Authorization", "Bearer "+login.Token)
			request.Header.Set("X-MDD-CSRF-Token", login.Session.CSRF)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("mutation HTTP %d", response.Code)
			}
			_ = writeEnvelope(ctx, socket, envelope{Kind: kindHello, Hello: &Hello{SchemaVersion: SchemaVersion, AgentID: "agent-1", ProcessGeneration: "process-1"}})
			if _, err := readEnvelope(ctx, socket); err == nil {
				t.Fatal("revoked handshake was acknowledged")
			}
			select {
			case <-served:
			case <-ctx.Done():
				t.Fatal("revoked handler did not return")
			}
			if _, present := server.Status("agent-1"); present {
				t.Fatal("revoked Agent was admitted")
			}
		})
	}
}

type cancellationAuthenticator struct{ entered, cancelled chan struct{} }

func (auth *cancellationAuthenticator) AuthenticateAKA(ctx context.Context, request AKARequest) AKAResponse {
	close(auth.entered)
	<-ctx.Done()
	close(auth.cancelled)
	return AKAResponse{OperationID: request.OperationID, Failure: &RemoteError{Kind: "transport", Code: "cancelled"}}
}

func TestDisconnectCancelsConnectionWorkBeforeWaiting(t *testing.T) {
	auth := &cancellationAuthenticator{entered: make(chan struct{}), cancelled: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		if _, err := readEnvelope(ctx, socket); err != nil {
			return
		}
		if err := writeEnvelope(ctx, socket, envelope{Kind: kindHelloAck}); err != nil {
			return
		}
		request := AKARequest{OperationID: "aka-1", SessionGeneration: "session-1", CardID: "8944000000000000001", Application: AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16)}
		if err := writeEnvelope(ctx, socket, envelope{Kind: kindAKARequest, RequestID: "rpc-1", AKARequest: &request}); err != nil {
			return
		}
		select {
		case <-auth.entered:
		case <-ctx.Done():
		}
	}))
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		done <- (Client{URL: strings.Replace(server.URL, "http", "ws", 1) + "/v1/agent/ws", Token: testToken,
			Hello: Hello{SchemaVersion: SchemaVersion, AgentID: "agent-1", ProcessGeneration: "process-1"}, Authenticator: auth, OperationTimeout: time.Minute}).Run(ctx)
	}()
	select {
	case <-auth.entered:
	case <-time.After(2 * time.Second):
		select {
		case err := <-done:
			t.Fatalf("client stopped before operation admission: %v", err)
		default:
			t.Fatal("executor did not receive the operation")
		}
	}
	select {
	case <-auth.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("socket failure did not cancel executor")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run still waits for the old operation timeout")
	}
	if ctx.Err() != nil {
		t.Fatal("test cancelled the whole Agent instead of its connection")
	}
}
