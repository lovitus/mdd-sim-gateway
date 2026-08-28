package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
)

func testReplay(t *testing.T, receivedAt time.Time) *events.Replay {
	t.Helper()
	replay, err := events.NewReplay(10 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	record := events.Record{ReceivedAt: receivedAt, Epoch: 1, Event: events.Event{
		SchemaVersion: events.SchemaVersion, EventID: "intent-1", LineID: "line-1",
		ProducerRole: events.RoleCore, ProducerID: "core-1", Layer: state.LayerIntent,
		Condition: state.ConditionReady, Available: true, Code: "line_enabled",
		Generation: "config-1", Sequence: 1, ObservedAt: receivedAt,
	}}
	if _, err := replay.Apply(record); err != nil {
		t.Fatal(err)
	}
	return replay
}

func TestLinesAreProjectedAtRequestTime(t *testing.T) {
	receivedAt := time.Unix(1_800_000_000, 0).UTC()
	now := receivedAt.Add(5 * time.Second)
	server := NewServer(testReplay(t, receivedAt), func() time.Time { return now })
	request := httptest.NewRequest(http.MethodGet, "/v1/lines/line-1", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var fresh events.LineProjection
	if err := json.Unmarshal(response.Body.Bytes(), &fresh); err != nil {
		t.Fatal(err)
	}
	if fact(t, fresh, state.LayerIntent).Condition != state.ConditionReady {
		t.Fatalf("fresh projection = %+v", fresh)
	}

	now = receivedAt.Add(11 * time.Second)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var stale events.LineProjection
	if err := json.Unmarshal(response.Body.Bytes(), &stale); err != nil {
		t.Fatal(err)
	}
	if got := fact(t, stale, state.LayerIntent); got.Condition != state.ConditionUnknown || got.Code != "stale" {
		t.Fatalf("stale fact = %+v", got)
	}
}

func TestReadOnlyServerRejectsMutationMethods(t *testing.T) {
	server := NewServer(testReplay(t, time.Now().UTC()), time.Now)
	request := httptest.NewRequest(http.MethodPost, "/v1/lines", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
}

func TestMissingLineUsesMachineErrorCode(t *testing.T) {
	server := NewServer(testReplay(t, time.Now().UTC()), time.Now)
	request := httptest.NewRequest(http.MethodGet, "/v1/lines/missing", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || response.Body.String() != "{\"code\":\"line_not_found\"}\n" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestOnlyLoopbackListenAddressesAreAccepted(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:8443", "localhost:9000"} {
		if !ValidateListenAddress(address) {
			t.Errorf("loopback address rejected: %s", address)
		}
	}
	for _, address := range []string{"0.0.0.0:8443", "[::]:8443", "10.44.0.23:8443", "bad"} {
		if ValidateListenAddress(address) {
			t.Errorf("non-loopback address accepted: %s", address)
		}
	}
}

func TestBrowserMediaSharesCoreListener(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/v1/media/") || !mediaproxy.AuthorizedToken(request.Header.Get("Authorization"), token) {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		socket, err := websocket.Accept(response, request, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		kind, payload, err := socket.Read(context.Background())
		if err == nil {
			_ = socket.Write(context.Background(), kind, payload)
		}
	}))
	defer provider.Close()
	providers := mediaauth.NewProviderDirectory()
	if err := providers.Replace(mediaauth.Provider{LineID: "line-1", Generation: "provider-1", BaseURL: "ws" + strings.TrimPrefix(provider.URL, "http"), Token: token}); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(t.TempDir(), "auth.json")
	authPayload := `{"version":1,"username":"fanli","salt":"00112233445566778899aabbccddeeff","password_hash":"ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302"}`
	if err := os.WriteFile(authPath, []byte(authPayload), 0o600); err != nil {
		t.Fatal(err)
	}
	authManager, err := adminauth.NewManager(authPath, false, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	authHandler, err := adminauth.NewHandler(authManager)
	if err != nil {
		t.Fatal(err)
	}
	router, err := mediaauth.NewRouter(authManager, providers, time.Now, 2)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := mediaproxy.NewHandler(router, nil, time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(testReplay(t, time.Now().UTC()), time.Now,
		WithAdminAuth(authHandler), WithManagementAuth(authManager.Middleware), WithBrowserMedia(relay)))
	defer server.Close()
	if response, err := http.Get(server.URL + "/healthz"); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("health response=%v err=%v", response, err)
	} else {
		_ = response.Body.Close()
	}
	loginRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/auth/login", strings.NewReader(`{"username":"fanli","password":"correct horse battery staple"}`))
	loginResponse, err := http.DefaultClient.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusOK || len(loginResponse.Cookies()) != 1 {
		t.Fatalf("login status=%d cookies=%v", loginResponse.StatusCode, loginResponse.Cookies())
	}
	cookie := loginResponse.Cookies()[0]
	authSession, found := authManager.Session(cookie.Value)
	if !found {
		t.Fatal("login session was not stored")
	}
	linesRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/lines", nil)
	if linesResponse, err := http.DefaultClient.Do(linesRequest); err != nil || linesResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated lines response=%v err=%v", linesResponse, err)
	} else {
		_ = linesResponse.Body.Close()
	}
	linesRequest.AddCookie(cookie)
	if linesResponse, err := http.DefaultClient.Do(linesRequest); err != nil || linesResponse.StatusCode != http.StatusOK {
		t.Fatalf("authenticated lines response=%v err=%v", linesResponse, err)
	} else {
		_ = linesResponse.Body.Close()
	}
	lease, err := router.Issue(mediaauth.LeaseRequest{Subject: authSession.Subject, LineID: "line-1", CallID: "call-1", ProviderGeneration: "provider-1", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	options := &websocket.DialOptions{HTTPHeader: http.Header{
		"Cookie": {cookie.String()}, "Origin": {server.URL},
	}}
	mediaURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/browser-media/" + lease.SessionID + "/ws"
	if _, response, err := websocket.Dial(context.Background(), mediaURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}}); err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated response=%v err=%v", response, err)
	}
	socket, _, err := websocket.Dial(context.Background(), mediaURL, options)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	if err := socket.Write(context.Background(), websocket.MessageBinary, make([]byte, 320)); err != nil {
		t.Fatal(err)
	}
	if kind, payload, err := socket.Read(context.Background()); err != nil || kind != websocket.MessageBinary || len(payload) != 320 {
		t.Fatalf("kind=%v len=%d err=%v", kind, len(payload), err)
	}
	_ = socket.Close(websocket.StatusNormalClosure, "test complete")
	if err := providers.Replace(mediaauth.Provider{LineID: "line-1", Generation: "provider-2", BaseURL: "ws" + strings.TrimPrefix(provider.URL, "http"), Token: token}); err != nil {
		t.Fatal(err)
	}
	if _, response, err := websocket.Dial(context.Background(), mediaURL, options); err == nil || response == nil || response.StatusCode != http.StatusConflict {
		t.Fatalf("stale generation response=%v err=%v", response, err)
	}
}

func fact(t *testing.T, projection events.LineProjection, layer state.Layer) state.FactView {
	t.Helper()
	for _, item := range projection.Facts {
		if item.Layer == layer {
			return item
		}
	}
	t.Fatalf("missing layer %s", layer)
	return state.FactView{}
}
