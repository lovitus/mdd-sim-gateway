// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	swusim "github.com/boa-z/vowifi-go/engine/sim"
	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/agentaka"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/ims"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/service"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
	"github.com/pion/rtp"
)

const processTestToken = "0123456789abcdef0123456789abcdef"
const processTestAgentToken = "abcdef0123456789abcdef0123456789"
const processTestRegistrationToken = "fedcba9876543210fedcba9876543210"

func TestProviderProcessCarriesCallOverCoreWebSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt is not implemented on Windows")
	}
	directory := t.TempDir()
	address := unusedLoopbackAddress(t)
	agentAPI, err := agentlink.NewBrokerAPI(processFakeAgent{}, processTestAgentToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	agentServer := httptest.NewServer(agentAPI)
	defer agentServer.Close()
	providerDirectory := mediaauth.NewProviderDirectory()
	registrationAPI, err := mediaauth.NewRegistrationHandler(providerDirectory, processTestRegistrationToken)
	if err != nil {
		t.Fatal(err)
	}
	registrationServer := httptest.NewServer(registrationAPI)
	defer registrationServer.Close()
	settings := processTestConfig(address, filepath.Join(directory, "operations.db"), agentServer.URL+"/v1/agent/aka")
	settings.Core.RegistrationURL = registrationServer.URL + "/v1/media/providers"
	settings.Core.RegistrationToken = processTestRegistrationToken
	settings.Core.RefreshMS = 1000
	configPath := filepath.Join(directory, "config.json")
	wire, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, wire, 0o600); err != nil {
		t.Fatal(err)
	}

	var processLog lockedBuffer
	command := exec.Command(os.Args[0], "-test.run=^TestProviderProcessHelper$")
	command.Env = append(os.Environ(), "MDD_VOWIFI_PROCESS_HELPER=1", "MDD_VOWIFI_PROCESS_CONFIG="+configPath)
	command.Stdout, command.Stderr = &processLog, &processLog
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	baseURL := "http://" + address
	waitForHealth(t, baseURL, command, &processLog)
	waitForProviderGeneration(t, providerDirectory, "line-process", true)
	client, err := vowifiipc.NewClient(baseURL, processTestToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := client.Start(ctx, vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatalf("start runtime: %v\n%s", err, processLog.String())
	}

	proxy, err := mediaproxy.NewHandler(mediaproxy.AuthorizerFunc(func(context.Context, *http.Request) (mediaproxy.Target, error) {
		return mediaproxy.Target{URL: "ws://" + address + "/v1/media/call-process", Token: processTestToken}, nil
	}), nil, time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	core := httptest.NewServer(proxy)
	defer core.Close()
	browser, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(core.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.CloseNow()
	writeProcessJSON(t, ctx, browser, map[string]any{
		"type": "browser.media.hello", "version": 1, "session_id": "call-process", "ticket": "ticket-1",
	})
	claimed := readProcessJSON(t, ctx, browser)
	challenge, _ := claimed["challenge"].(string)
	if claimed["type"] != "browser.media.claimed" || challenge == "" {
		t.Fatalf("claimed=%v", claimed)
	}
	if started := readProcessJSON(t, ctx, browser); started["type"] != "browser.media.started" {
		t.Fatalf("started=%v", started)
	}
	pcm := processSignalFrame()
	for index := 0; index < 2; index++ {
		if err := browser.Write(ctx, websocket.MessageBinary, pcm); err != nil {
			t.Fatal(err)
		}
		if kind, echoed, err := browser.Read(ctx); err != nil || kind != websocket.MessageBinary || string(echoed) != string(pcm) {
			t.Fatalf("canary kind=%v len=%d err=%v", kind, len(echoed), err)
		}
	}
	writeProcessJSON(t, ctx, browser, map[string]any{
		"type": "browser.media.evidence", "version": 1, "challenge": challenge,
		"capture_callbacks": 2, "playback_callbacks": 2, "played_frames": 2,
	})
	if status := readProcessJSON(t, ctx, browser); status["ready"] != true {
		t.Fatalf("status=%v", status)
	}
	if ready := readProcessJSON(t, ctx, browser); ready["type"] != "browser.media.ready" {
		t.Fatalf("ready=%v", ready)
	}

	call, err := client.StartCall(ctx, vowifiipc.StartCallRequest{
		OperationID: "call-start", CallID: "call-process", Callee: "+1000000", MediaBufferMS: 500,
	})
	if err != nil || call.Status.ActiveCall == nil || call.Status.ActiveCall.Condition != vowifiipc.CallActive {
		t.Fatalf("start call=%+v err=%v\n%s", call, err, processLog.String())
	}
	if err := browser.Write(ctx, websocket.MessageBinary, pcm); err != nil {
		t.Fatal(err)
	}
	if !readNonSilentPCM(t, ctx, browser) {
		t.Fatal("fake RTP round trip did not return non-silent browser PCM")
	}
	ended, err := client.EndCall(ctx, vowifiipc.EndCallRequest{
		OperationID: "call-end", CallID: "call-process", ReasonCode: "test_complete",
	})
	if err != nil || ended.Status.ActiveCall != nil {
		t.Fatalf("end call=%+v err=%v\n%s", ended, err, processLog.String())
	}
	if _, err := client.Stop(ctx, vowifiipc.LifecycleRequest{OperationID: "runtime-stop"}); err != nil {
		t.Fatalf("stop runtime: %v\n%s", err, processLog.String())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("provider process exit: %v\n%s", err, processLog.String())
	}
	waitForProviderGeneration(t, providerDirectory, "line-process", false)
}

func waitForProviderGeneration(t *testing.T, directory *mediaauth.ProviderDirectory, lineID string, expected bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, found := directory.CurrentGeneration(lineID)
		if found == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, found := directory.CurrentGeneration(lineID)
	t.Fatalf("provider registration present=%v, want %v", found, expected)
}

func TestProviderProcessHelper(t *testing.T) {
	if os.Getenv("MDD_VOWIFI_PROCESS_HELPER") != "1" {
		return
	}
	settings, err := loadConfig(os.Getenv("MDD_VOWIFI_PROCESS_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runWithFactory(settings, processFakeFactory{settings: settings}); err != nil {
		t.Fatal(err)
	}
}

type processFakeFactory struct{ settings config }

func (factory processFakeFactory) Start(context.Context) (service.Runtime, error) {
	authenticator, err := agentaka.New(agentlink.BrokerClient{
		URL: factory.settings.Agent.BrokerURL, Token: factory.settings.Agent.BrokerToken,
	}, agentaka.Config{
		AgentID: factory.settings.Agent.ID, ProcessGeneration: factory.settings.Agent.ProcessGeneration,
		SessionGeneration: factory.settings.Agent.SessionGeneration, CardID: factory.settings.Agent.CardID,
		Timeout: time.Second,
	})
	if err != nil {
		return nil, err
	}
	if _, err := authenticator.AuthenticateAKA(swusim.AKAAuthRequest{
		Application: swusim.AKAApplicationUSIM, RAND: bytes.Repeat([]byte{0x40}, 16), AUTN: bytes.Repeat([]byte{0x50}, 16),
	}); err != nil {
		return nil, err
	}
	clientPackets, serverPackets := processPacketPair()
	clientStack, err := usernet.Open(context.Background(), clientPackets, usernet.Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")},
	})
	if err != nil {
		return nil, err
	}
	serverStack, err := usernet.Open(context.Background(), serverPackets, usernet.Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.2")},
	})
	if err != nil {
		_ = clientStack.Close(context.Background())
		return nil, err
	}
	pcscf, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5060")
	if err != nil {
		return nil, errors.Join(err, clientStack.Close(context.Background()), serverStack.Close(context.Background()))
	}
	mediaSocket, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5000")
	if err != nil {
		_ = pcscf.Close()
		return nil, errors.Join(err, clientStack.Close(context.Background()), serverStack.Close(context.Background()))
	}
	go serveProcessPCSCF(pcscf)
	go echoProcessRTP(mediaSocket)
	registrar, err := ims.NewRegistrar(clientStack, runtimehost.WireIMSRegistrar{
		Network: "udp4", ServerAddr: "10.0.0.2:5060", ContactHost: "10.0.0.1",
		Expires: 600, DisableRefresh: true, DisableKeepalive: true, Timeout: 3 * time.Second,
	})
	if err != nil {
		_ = pcscf.Close()
		_ = mediaSocket.Close()
		return nil, errors.Join(err, clientStack.Close(context.Background()), serverStack.Close(context.Background()))
	}
	registration, err := registrar.RegisterIMS(context.Background(), runtimehost.IMSRegistrationConfig{
		DeviceID: "device-process", TraceID: "trace-process",
		Profile: identity.Profile{IMSI: "001010123456789", MCC: "001", MNC: "01"},
		Prepared: &identity.PreparedSession{IMSIdentity: identity.IMSIdentityResolution{
			IMPI: "user@ims.test", IMPU: "sip:user@ims.test", Domain: "ims.test",
		}},
	})
	if err != nil {
		_ = pcscf.Close()
		_ = mediaSocket.Close()
		return nil, errors.Join(err, clientStack.Close(context.Background()), serverStack.Close(context.Background()))
	}
	return &processVoiceRuntime{
		client: clientStack, server: serverStack, registration: registration,
		pcscf: pcscf, mediaSocket: mediaSocket,
	}, nil
}

type processVoiceRuntime struct {
	client, server *usernet.Stack
	registration   runtimehost.IMSRegistrationResult
	pcscf          net.PacketConn
	mediaSocket    net.PacketConn
	once           sync.Once
}

func (runtime *processVoiceRuntime) Layers() service.Layers {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	return service.Layers{Tunnel: ready, IMS: ready, Voice: ready, Messaging: ready}
}

func (runtime *processVoiceRuntime) StartMediaCall(ctx context.Context, request vowifiipc.StartCallRequest) (service.VoiceCall, error) {
	agent, err := ims.NewOutboundAgent(runtime.registration)
	if err != nil {
		return nil, err
	}
	call, result, err := ims.StartMediaCall(ctx, agent, runtime.client, ims.MediaCallConfig{
		LocalRTP: "10.0.0.1:0", LocalRTCP: "10.0.0.1:0",
		Codec: media.CodecPCMU, BufferMS: request.MediaBufferMS,
	}, voicehost.OutboundCallRequest{
		DeviceID: "device-process", CallID: request.CallID, Callee: request.Callee,
	})
	if err != nil {
		return nil, err
	}
	if !result.Accepted {
		return nil, errors.New("fake P-CSCF rejected call")
	}
	return call, nil
}

func (runtime *processVoiceRuntime) Close(ctx context.Context) error {
	var result error
	runtime.once.Do(func() {
		var registrationErr error
		if runtime.registration.Close != nil {
			registrationErr = runtime.registration.Close(ctx)
		}
		result = errors.Join(registrationErr, runtime.pcscf.Close(), runtime.mediaSocket.Close(),
			runtime.client.Close(ctx), runtime.server.Close(ctx))
	})
	return result
}

type processPacketSession struct {
	inbound chan []byte
	peer    *processPacketSession
	closed  chan struct{}
	once    sync.Once
}

func processPacketPair() (*processPacketSession, *processPacketSession) {
	first := &processPacketSession{inbound: make(chan []byte, 256), closed: make(chan struct{})}
	second := &processPacketSession{inbound: make(chan []byte, 256), closed: make(chan struct{})}
	first.peer, second.peer = second, first
	return first, second
}

func (session *processPacketSession) Send(ctx context.Context, packet []byte) error {
	copyOfPacket := append([]byte(nil), packet...)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.peer.closed:
		return usernet.ErrClosed
	case session.peer.inbound <- copyOfPacket:
		return nil
	}
}

func (session *processPacketSession) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-session.closed:
		return nil, usernet.ErrClosed
	case packet := <-session.inbound:
		return append([]byte(nil), packet...), nil
	}
}

func (session *processPacketSession) Close(context.Context) error {
	session.once.Do(func() { close(session.closed) })
	return nil
}

func serveProcessPCSCF(connection net.PacketConn) {
	buffer := make([]byte, 65535)
	for {
		size, source, err := connection.ReadFrom(buffer)
		if err != nil {
			return
		}
		request, err := voiceclient.ParseSIPRequest(buffer[:size])
		if err != nil || request.Method == "ACK" {
			continue
		}
		headers := map[string]string{}
		var body []byte
		switch request.Method {
		case "REGISTER":
			headers["P-Associated-URI"] = "<sip:user@ims.test>"
		case "INVITE":
			headers["To"] = processFirstHeader(request.Headers, "To") + ";tag=pcscf"
			headers["Contact"] = "<sip:callee@10.0.0.2:5060>"
			headers["Content-Type"] = "application/sdp"
			body = []byte("v=0\r\no=- 2 2 IN IP4 10.0.0.2\r\ns=-\r\nc=IN IP4 10.0.0.2\r\nt=0 0\r\nm=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n")
		}
		wire, err := voiceclient.BuildSIPResponseWire(request, 200, "OK", headers, body)
		if err == nil {
			_, _ = connection.WriteTo(wire, source)
		}
	}
}

func echoProcessRTP(connection net.PacketConn) {
	buffer := make([]byte, 2048)
	for {
		size, source, err := connection.ReadFrom(buffer)
		if err != nil {
			return
		}
		var packet rtp.Packet
		if packet.Unmarshal(buffer[:size]) != nil {
			continue
		}
		packet.SSRC = 0x10203040
		wire, err := packet.Marshal()
		if err == nil {
			_, _ = connection.WriteTo(wire, source)
		}
	}
}

func processTestConfig(address, statePath, brokerURL string) config {
	settings := config{LineID: "line-process", ProviderID: "native", DeviceID: "device-process", TraceID: "trace-process"}
	settings.IPC.Listen, settings.IPC.Token, settings.IPC.StatePath = address, processTestToken, statePath
	settings.IPC.OperationTimeoutMS, settings.IPC.ShutdownTimeoutMS = 8000, 5000
	settings.Agent.BrokerURL = brokerURL
	settings.Agent.BrokerToken = processTestAgentToken
	settings.Agent.ID, settings.Agent.ProcessGeneration = "agent-1", "agent-process-1"
	settings.Agent.SessionGeneration, settings.Agent.CardID = "card-session-1", "8944100000000000001"
	settings.SIM.IMSI, settings.SIM.MCC, settings.SIM.MNC = "001010123456789", "001", "01"
	settings.Network.EPDGAddress = "epdg.invalid"
	return settings
}

type processFakeAgent struct{}

func (processFakeAgent) AuthenticateAKA(_ context.Context, agentID, generation string, request agentlink.AKARequest) (agentlink.AKAResponse, error) {
	if agentID != "agent-1" || generation != "agent-process-1" {
		return agentlink.AKAResponse{}, errors.New("wrong fake Agent owner")
	}
	if request.SessionGeneration != "card-session-1" || request.CardID != "8944100000000000001" {
		return agentlink.AKAResponse{}, errors.New("wrong fake card owner")
	}
	body := []byte{0xDB, 0x08}
	body = append(body, bytes.Repeat([]byte{0x10}, 8)...)
	body = append(body, 0x10)
	body = append(body, bytes.Repeat([]byte{0x20}, 16)...)
	body = append(body, 0x10)
	body = append(body, bytes.Repeat([]byte{0x30}, 16)...)
	return agentlink.AKAResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
		Body: body, SW1: 0x90, SW2: 0x00,
	}, nil
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitForHealth(t *testing.T, baseURL string, command *exec.Cmd, log *lockedBuffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if command.ProcessState != nil {
			t.Fatalf("provider exited early\n%s", log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("provider health timed out\n%s", log.String())
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func writeProcessJSON(t *testing.T, ctx context.Context, socket *websocket.Conn, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func readProcessJSON(t *testing.T, ctx context.Context, socket *websocket.Conn) map[string]any {
	t.Helper()
	kind, payload, err := socket.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("control kind=%v err=%v", kind, err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func readNonSilentPCM(t *testing.T, ctx context.Context, socket *websocket.Conn) bool {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		kind, payload, err := socket.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.MessageBinary && processHasSignal(payload) {
			return true
		}
	}
	return false
}

func processSignalFrame() []byte {
	frame := make([]byte, media.PCMFrameBytes)
	for index := 0; index < media.FrameSamples; index++ {
		binary.LittleEndian.PutUint16(frame[index*2:], uint16(int16(index*83-6000)))
	}
	return frame
}

func processHasSignal(frame []byte) bool {
	count := 0
	for index := 0; index+1 < len(frame); index += 2 {
		value := int16(binary.LittleEndian.Uint16(frame[index:]))
		if value > 128 || value < -128 {
			count++
			if count >= 8 {
				return true
			}
		}
	}
	return false
}

func processFirstHeader(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}
