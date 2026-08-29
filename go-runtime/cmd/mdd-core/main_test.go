package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/core"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/pintls"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerfacts"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"golang.org/x/crypto/scrypt"
)

const (
	processPassword = "core-process-password"
	processToken    = "0123456789abcdef0123456789abcdef"
	localToken      = "abcdef0123456789abcdef0123456789"
)

type processAuthenticator struct{}

func (processAuthenticator) AuthenticateAKA(_ context.Context, request agentlink.AKARequest) agentlink.AKAResponse {
	return agentlink.AKAResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
		Body: []byte{0xdb, 0x02, 1, 2}, SW1: 0x90, SW2: 0x00,
	}
}

func TestCoreProcessHelper(t *testing.T) {
	path := os.Getenv("MDD_CORE_TEST_CONFIG")
	if path == "" {
		return
	}
	settings, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	go func() {
		<-interrupt
		stop()
	}()
	if err := run(ctx, settings); err != nil {
		t.Fatal(err)
	}
}

func TestLiveCoreProcessUsesOnePublicTLSListenerAndLoopbackIPC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process interrupt test requires Unix signal behavior")
	}
	root := t.TempDir()
	publicAddress := availableAddress(t)
	localAddress := availableAddress(t)
	certPath, keyPath, fingerprint := testTLSIdentity(t, root)
	authPath := writeAuth(t, root)
	configPath := filepath.Join(root, "core.json")
	settings := config{}
	settings.Public.Listen = publicAddress
	settings.Public.TLSCert = certPath
	settings.Public.TLSKey = keyPath
	settings.Local.Listen = localAddress
	settings.Local.Token = localToken
	settings.AuthPath = authPath
	settings.EventsPath = filepath.Join(root, "events.db")
	settings.CatalogPath = filepath.Join(root, "lines.db")
	settings.TTLSeconds = 30
	catalog, err := linecatalog.Open(settings.CatalogPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Put(linecatalog.Line{
		ID: "line-1", Enabled: true, CardID: "89440001",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(settings)
	if err := os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestCoreProcessHelper$")
	command.Env = append(os.Environ(), "MDD_CORE_TEST_CONFIG="+configPath)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	httpClient, err := pintls.NewHTTPClient("wss://"+publicAddress+"/v1/agent/ws", fingerprint, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	publicURL := "https://" + publicAddress
	waitForCore(t, httpClient, publicURL, command, &stderr)
	assertEmbeddedWebUI(t, httpClient, publicURL)
	cookie, csrf := login(t, httpClient, publicURL)
	readSystemDiagnostics(t, httpClient, publicURL, cookie, fingerprint, false)

	agentContext, stopAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- (agentlink.Client{
			URL: "wss://" + publicAddress + "/v1/agent/ws", Token: processToken,
			Hello:      agentlink.Hello{SchemaVersion: 1, AgentID: "agent-1", ProcessGeneration: "process-1"},
			HTTPClient: httpClient, Authenticator: processAuthenticator{}, OperationTimeout: time.Second,
			Health: func() agentlink.TopologySnapshot {
				return agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{{
					ReaderName: "reader-1", CardPresent: true, SessionGeneration: "card-1",
					CardID: "89440001", IdentityState: agentlink.CardIdentified,
				}}}
			},
		}).Run(agentContext)
	}()
	brokerRoundTrip(t, localAddress)
	waitForAgentFacts(t, httpClient, publicURL, cookie)
	readBrowserSnapshot(t, httpClient, publicURL, publicAddress, cookie)

	provider := echoProvider(t)
	defer provider.Close()
	registration := mediaauth.RegistrationClient{
		URL: "http://" + localAddress + "/v1/media/providers", Token: localToken,
	}
	if err := registration.Register(context.Background(), mediaauth.Provider{
		LineID: "line-1", ProviderID: "provider-1", Generation: "provider-1", BaseURL: provider.URL,
		Token: processToken,
	}); err != nil {
		t.Fatal(err)
	}
	readSystemDiagnostics(t, httpClient, publicURL, cookie, fingerprint, true)
	if err := (providerfacts.Client{
		URL: "http://" + localAddress + "/v1/provider/facts", Token: localToken,
	}).Report(context.Background(), (processProviderBackend{}).snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := (providermessages.Client{
		URL: "http://" + localAddress + "/v1/provider/messages", Token: localToken,
	}).Report(context.Background(), providermessages.Event{
		SchemaVersion: providermessages.SchemaVersion, EventID: "provider-1:received:sip-1:1",
		LineID: "line-1", ProviderID: "provider-1", ProcessGeneration: "provider-1",
		Kind: providermessages.KindReceived, ObservedAt: time.Now(), Sender: "+100", Recipient: "+200", Body: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	readApplyPreflight(t, localAddress)
	readBrowserProviderFacts(t, httpClient, publicURL, publicAddress, cookie)
	readBrowserMessages(t, httpClient, publicURL, publicAddress, cookie)
	startProviderRuntime(t, httpClient, publicURL, cookie, csrf)
	sendProviderMessage(t, httpClient, publicURL, cookie, csrf)
	sessionPath := issueLease(t, httpClient, publicURL, cookie, csrf)
	headers := http.Header{
		"Cookie": {cookie.Name + "=" + cookie.Value},
		"Origin": {publicURL},
	}
	socket, response, err := websocket.Dial(context.Background(), "wss://"+publicAddress+sessionPath, &websocket.DialOptions{
		HTTPClient: httpClient, HTTPHeader: headers,
	})
	if err != nil {
		t.Fatalf("browser media dial response=%v err=%v", response, err)
	}
	message := []byte(`{"type":"browser.media.hello","version":1}`)
	if err := socket.Write(context.Background(), websocket.MessageText, message); err != nil {
		t.Fatal(err)
	}
	kind, returned, err := socket.Read(context.Background())
	if err != nil || kind != websocket.MessageText || !bytes.Equal(returned, message) {
		t.Fatalf("media round trip kind=%v payload=%s err=%v", kind, returned, err)
	}
	_ = socket.Close(websocket.StatusNormalClosure, "done")
	stopAgent()
	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent client did not stop")
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("Core process exit: %v\n%s", err, stderr.String())
	}
	stopped = true
}

func assertEmbeddedWebUI(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	for _, test := range []struct {
		path, contentType, marker string
	}{
		{"/", "text/html; charset=utf-8", "MDD Go Console"},
		{"/assets/app.js", "text/javascript; charset=utf-8", "/v1/browser/ws"},
	} {
		response, err := client.Get(baseURL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != test.contentType ||
			!strings.Contains(string(payload), test.marker) || response.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("WebUI %s status=%d type=%q payload=%q", test.path, response.StatusCode, response.Header.Get("Content-Type"), payload)
		}
	}
	response, err := client.Get(baseURL + "/api/not-a-route")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown API status=%d, want 404", response.StatusCode)
	}
}

func readApplyPreflight(t *testing.T, localAddress string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, "http://"+localAddress+providerapply.Path, nil)
	request.Header.Set("Authorization", "Bearer "+localToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var snapshot providerapply.Snapshot
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&snapshot) != nil ||
		snapshot.CatalogRevision != 2 || len(snapshot.Lines) != 1 || snapshot.Lines[0].Code != "provider_reachable" ||
		snapshot.Lines[0].ProcessGeneration != "provider-1" || snapshot.Lines[0].ActiveCall != nil {
		t.Fatalf("apply preflight status=%d snapshot=%+v", response.StatusCode, snapshot)
	}
}

func TestLoadConfigRejectsLoosePermissionsAndUnknownFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "core.json")
	payload := `{"public":{"listen":"127.0.0.1:8443","tls_cert":"/cert","tls_key":"/key"},"local":{"listen":"127.0.0.1:9081","token":"0123456789abcdef0123456789abcdef"},"auth_path":"/auth","events_path":"/events","extra":true}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("unknown configuration field was accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "0600") {
			t.Fatalf("loose permission error=%v", err)
		}
	}
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func testTLSIdentity(t *testing.T, root string) (string, string, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mdd-core-test"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(root, "server.crt"), filepath.Join(root, "server.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(certificate)
	return certPath, keyPath, hex.EncodeToString(fingerprint[:])
}

func readSystemDiagnostics(t *testing.T, client *http.Client, baseURL string, cookie *http.Cookie, fingerprint string, providerExpected bool) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/system/runtime", nil)
	request.AddCookie(cookie)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeInfo core.RuntimeInfo
	decodeErr := json.NewDecoder(response.Body).Decode(&runtimeInfo)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || runtimeInfo.Public.ListenerCount != 1 ||
		runtimeInfo.Public.Transport != "https+wss" || runtimeInfo.Public.TLSFingerprintSHA256 != fingerprint ||
		runtimeInfo.Local.Scope != "literal_loopback" {
		t.Fatalf("runtime status=%d info=%+v err=%v", response.StatusCode, runtimeInfo, decodeErr)
	}
	request, _ = http.NewRequest(http.MethodGet, baseURL+"/v1/diagnostics", nil)
	request.AddCookie(cookie)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics core.DiagnosticsSnapshot
	decodeErr = json.NewDecoder(response.Body).Decode(&diagnostics)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil {
		t.Fatalf("diagnostics status=%d snapshot=%+v err=%v", response.StatusCode, diagnostics, decodeErr)
	}
	want := "fail:provider_route_unavailable"
	if providerExpected {
		want = "pass:provider_route_current"
	}
	found := false
	for _, check := range diagnostics.Checks {
		if check.ID == "line.line-1.provider_route" {
			found = true
			if actual := check.Status + ":" + check.Code; actual != want {
				t.Fatalf("provider route=%s want %s", actual, want)
			}
		}
	}
	if !found {
		t.Fatal("provider route diagnostic was omitted")
	}
}

func writeAuth(t *testing.T, root string) string {
	t.Helper()
	salt := []byte("0123456789abcdef")
	hash, err := scrypt.Key([]byte(processPassword), salt, 1<<15, 8, 1, 32)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"version": 1, "username": "fanli", "salt": hex.EncodeToString(salt),
		"password_hash": hex.EncodeToString(hash), "agent_token": processToken,
	})
	path := filepath.Join(root, "auth.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForCore(t *testing.T, client *http.Client, baseURL string, command *exec.Cmd, stderr *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if command.ProcessState != nil {
			t.Fatalf("Core exited before ready: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Core did not become ready: %s", stderr.String())
}

func login(t *testing.T, client *http.Client, baseURL string) (*http.Cookie, string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/api/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"fanli","password":%q}`, processPassword)))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		CSRF string `json:"csrf"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK || len(response.Cookies()) != 1 {
		t.Fatalf("login status=%d cookies=%v result=%+v err=%v", response.StatusCode, response.Cookies(), result, err)
	}
	return response.Cookies()[0], result.CSRF
}

func brokerRoundTrip(t *testing.T, address string) {
	t.Helper()
	request := agentlink.BrokerRequest{
		AKA: agentlink.AKAChallenge{OperationID: "aka-1", CardID: "89440001", Application: agentlink.AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16)},
	}
	payload, _ := json.Marshal(request)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		httpRequest, _ := http.NewRequest(http.MethodPost, "http://"+address+"/v1/agent/aka", bytes.NewReader(payload))
		httpRequest.Header.Set("Authorization", "Bearer "+localToken)
		response, err := http.DefaultClient.Do(httpRequest)
		if err == nil {
			var result agentlink.AKAResponse
			decodeErr := json.NewDecoder(response.Body).Decode(&result)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && result.SW1 == 0x90 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Agent AKA did not traverse public WSS and local broker")
}

func waitForAgentFacts(t *testing.T, client *http.Client, baseURL string, cookie *http.Cookie) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/agents", nil)
		request.AddCookie(cookie)
		response, err := client.Do(request)
		if err == nil {
			var result struct {
				Agents []agentlink.ConnectionStatus `json:"agents"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&result)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && len(result.Agents) == 1 &&
				result.Agents[0].AgentID == "agent-1" && result.Agents[0].Topology != nil &&
				result.Agents[0].Topology.ReaderCondition == agentlink.ReaderReady {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Agent health did not traverse public WSS into the authenticated management API")
}

func readBrowserSnapshot(t *testing.T, client *http.Client, baseURL, address string, cookie *http.Cookie) {
	t.Helper()
	socket, response, err := websocket.Dial(context.Background(), "wss://"+address+"/ws?auth_close=1", &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{"Cookie": {cookie.String()}, "Origin": {baseURL}},
	})
	if err != nil {
		t.Fatalf("browser state dial response=%v err=%v", response, err)
	}
	defer socket.CloseNow()
	var snapshot core.BrowserSnapshot
	if err := wsjson.Read(context.Background(), socket, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Type != "browser.snapshot" || snapshot.SchemaVersion != 1 || snapshot.Sequence != 1 ||
		len(snapshot.Agents) != 1 || snapshot.Agents[0].AgentID != "agent-1" || snapshot.Agents[0].Topology == nil {
		t.Fatalf("browser state snapshot=%+v", snapshot)
	}
	_ = socket.Close(websocket.StatusNormalClosure, "test complete")
}

func readBrowserProviderFacts(t *testing.T, client *http.Client, baseURL, address string, cookie *http.Cookie) {
	t.Helper()
	socket, response, err := websocket.Dial(context.Background(), "wss://"+address+"/ws?auth_close=1", &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{"Cookie": {cookie.String()}, "Origin": {baseURL}},
	})
	if err != nil {
		t.Fatalf("browser state dial response=%v err=%v", response, err)
	}
	defer socket.CloseNow()
	var snapshot core.BrowserSnapshot
	if err := wsjson.Read(context.Background(), socket, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, line := range snapshot.Lines {
		if line.LineID != "line-1" {
			continue
		}
		for _, fact := range line.Facts {
			if fact.Layer == state.LayerIMSVoice && fact.Condition == state.ConditionReady && fact.Available && fact.Fresh {
				_ = socket.Close(websocket.StatusNormalClosure, "test complete")
				return
			}
		}
	}
	t.Fatalf("browser snapshot did not contain fresh provider voice fact: %+v", snapshot.Lines)
}

func readBrowserMessages(t *testing.T, client *http.Client, baseURL, address string, cookie *http.Cookie) {
	t.Helper()
	socket, response, err := websocket.Dial(context.Background(), "wss://"+address+"/ws?auth_close=1", &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{"Cookie": {cookie.String()}, "Origin": {baseURL}},
	})
	if err != nil {
		t.Fatalf("browser state dial response=%v err=%v", response, err)
	}
	defer socket.CloseNow()
	var snapshot core.BrowserSnapshot
	if err := wsjson.Read(context.Background(), socket, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].LineID != "line-1" ||
		snapshot.Messages[0].Kind != providermessages.KindReceived || snapshot.Messages[0].Body != "hello" {
		t.Fatalf("browser messages=%+v", snapshot.Messages)
	}
	_ = socket.Close(websocket.StatusNormalClosure, "test complete")
}

func echoProvider(t *testing.T) *httptest.Server {
	t.Helper()
	api, err := vowifiipc.NewAPI(processProviderBackend{}, processToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/media/", http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/v1/media/") || request.Header.Get("Authorization") != "Bearer "+processToken {
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
	mux.Handle("/", api)
	server := httptest.NewServer(mux)
	server.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	return server
}

type processProviderBackend struct{}

func (processProviderBackend) snapshot() vowifiipc.Snapshot {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	return vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-1", ProviderID: "provider-1",
		ProcessGeneration: "provider-1", Sequence: 1, ObservedAt: time.Now().UTC(),
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning, Code: "ready"},
		Tunnel:  ready, IMS: ready, Voice: ready, Messaging: ready,
	}
}

func (backend processProviderBackend) Status(context.Context) (vowifiipc.Snapshot, error) {
	return backend.snapshot(), nil
}

func (backend processProviderBackend) Start(_ context.Context, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	return vowifiipc.OperationResult{OperationID: request.OperationID, Accepted: true, Code: "started", Status: backend.snapshot()}, nil
}

func (backend processProviderBackend) Stop(_ context.Context, request vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	return vowifiipc.OperationResult{OperationID: request.OperationID, Accepted: true, Code: "stopped", Status: backend.snapshot()}, nil
}

func (processProviderBackend) StartCall(context.Context, vowifiipc.StartCallRequest) (vowifiipc.CallResult, error) {
	return vowifiipc.CallResult{}, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "test_not_ready"}
}

func (processProviderBackend) EndCall(context.Context, vowifiipc.EndCallRequest) (vowifiipc.CallResult, error) {
	return vowifiipc.CallResult{}, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotReady, Code: "test_not_ready"}
}

func (backend processProviderBackend) SendMessage(_ context.Context, request vowifiipc.SendMessageRequest) (vowifiipc.MessageResult, error) {
	return vowifiipc.MessageResult{OperationResult: vowifiipc.OperationResult{
		OperationID: request.OperationID, Accepted: true, Code: "sent", Status: backend.snapshot(),
	}, MessageID: request.MessageID}, nil
}

func sendProviderMessage(t *testing.T, client *http.Client, baseURL string, cookie *http.Cookie, csrf string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/lines/line-1/vowifi/messages/send",
		strings.NewReader(`{"operation_id":"message-send-1","message_id":"message-1","recipient":"+44123","body":"hello"}`))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-MDD-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result vowifiipc.MessageResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK ||
		result.OperationID != "message-send-1" || result.MessageID != "message-1" || result.Code != "sent" ||
		result.Status.ProcessGeneration != "provider-1" {
		t.Fatalf("message send status=%d result=%+v err=%v", response.StatusCode, result, err)
	}
}

func startProviderRuntime(t *testing.T, client *http.Client, baseURL string, cookie *http.Cookie, csrf string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/lines/line-1/vowifi/runtime/start",
		strings.NewReader(`{"operation_id":"runtime-start-1"}`))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("provider mutation without CSRF status=%d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodPost, baseURL+"/v1/lines/line-1/vowifi/runtime/start",
		strings.NewReader(`{"operation_id":"runtime-start-1"}`))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-MDD-CSRF-Token", csrf)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result vowifiipc.OperationResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK ||
		result.OperationID != "runtime-start-1" || result.Status.ProcessGeneration != "provider-1" {
		t.Fatalf("provider mutation status=%d result=%+v err=%v", response.StatusCode, result, err)
	}
}

func issueLease(t *testing.T, client *http.Client, baseURL string, cookie *http.Cookie, csrf string) string {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/media/leases", strings.NewReader(`{"line_id":"line-1","call_id":"call-1"}`))
	request.AddCookie(cookie)
	request.Header.Set("X-MDD-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		WSPath string `json:"ws_path"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusCreated {
		t.Fatalf("lease status=%d result=%+v err=%v", response.StatusCode, result, err)
	}
	return result.WSPath
}
