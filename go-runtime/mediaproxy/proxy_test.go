package mediaproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestProxyPreservesTextAndBinaryMessageBoundaries(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/media/call-1" || !AuthorizedToken(request.Header.Get("Authorization"), testToken) {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		socket, err := websocket.Accept(response, request, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		for {
			kind, payload, err := socket.Read(context.Background())
			if err != nil {
				return
			}
			if err := socket.Write(context.Background(), kind, payload); err != nil {
				return
			}
		}
	}))
	defer provider.Close()
	handler, err := NewHandler(AuthorizerFunc(func(context.Context, *http.Request) (Target, error) {
		return Target{URL: "ws" + strings.TrimPrefix(provider.URL, "http") + "/v1/media/call-1", Token: testToken}, nil
	}), nil, time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	socket, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(proxy.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	assertRoundTrip(t, socket, websocket.MessageText, []byte(`{"type":"browser.media.hello","version":1}`))
	assertRoundTrip(t, socket, websocket.MessageBinary, make([]byte, 320))
}

func TestProxyRejectsUnauthorizedAndNonLoopbackTarget(t *testing.T) {
	denied, err := NewHandler(AuthorizerFunc(func(context.Context, *http.Request) (Target, error) {
		return Target{}, &AuthorizationError{Status: http.StatusUnauthorized, Code: "login_required"}
	}), nil, time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(denied)
	defer server.Close()
	_, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("response=%v err=%v", response, err)
	}
	invalid, err := NewHandler(AuthorizerFunc(func(context.Context, *http.Request) (Target, error) {
		return Target{URL: "ws://192.0.2.1:9000/v1/media/call-1", Token: testToken}, nil
	}), nil, time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	invalidServer := httptest.NewServer(invalid)
	defer invalidServer.Close()
	_, response, err = websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(invalidServer.URL, "http"), nil)
	if err == nil || response == nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("response=%v err=%v", response, err)
	}
}

func TestProxyBoundsMessagesAndKeepsOriginCheck(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		socket, err := websocket.Accept(response, request, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		_, _, _ = socket.Read(context.Background())
	}))
	defer provider.Close()
	handler, err := NewHandler(AuthorizerFunc(func(context.Context, *http.Request) (Target, error) {
		return Target{URL: "ws" + strings.TrimPrefix(provider.URL, "http") + "/v1/media/call-1", Token: testToken}, nil
	}), nil, time.Second, 320)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	socket, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.Write(context.Background(), websocket.MessageBinary, make([]byte, 321)); err != nil {
		t.Fatal(err)
	}
	_, _, err = socket.Read(context.Background())
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
	}

	headers := http.Header{"Origin": []string{"https://evil.example"}}
	_, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: headers})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin response=%v err=%v", response, err)
	}
}

func TestAuthorizedToken(t *testing.T) {
	if !AuthorizedToken("Bearer "+testToken, testToken) || AuthorizedToken("Bearer wrong", testToken) || AuthorizedToken(testToken, testToken) {
		t.Fatal("bearer comparison failed")
	}
}

func assertRoundTrip(t *testing.T, socket *websocket.Conn, kind websocket.MessageType, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := socket.Write(ctx, kind, payload); err != nil {
		t.Fatal(err)
	}
	gotKind, got, err := socket.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotKind != kind || string(got) != string(payload) {
		t.Fatalf("kind=%v payload=%x", gotKind, got)
	}
}
