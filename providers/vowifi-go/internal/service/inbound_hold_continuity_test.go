// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/browsermedia"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/ims"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
	"github.com/pion/rtp"
	"github.com/zaf/g711"
)

type holdMediaDirectory struct{ *browsermedia.Registry }

func (directory holdMediaDirectory) Lookup(id string) (BrowserMediaSession, bool) {
	return directory.Session(id)
}

// Only registration/carrier termination are fixtures. Incoming signalling,
// runtime adaptation, PCM/RTP, WebSocket sessions and Backend guard are real.
type holdRuntime struct{ *upstreamRuntime }

func (*holdRuntime) Layers() Layers { return *fakeRecoveryLayers() }

type holdTerminator struct{ ended chan string }

func (peer holdTerminator) EndCarrierCallWithResult(_ context.Context, id string) (voicehost.DialogInfoResult, error) {
	peer.ended <- id
	return voicehost.DialogInfoResult{Accepted: true, StatusCode: 200}, nil
}

func TestIncomingHoldKeepsWebSocketAliveWithoutForgingClientHeartbeat(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	packets, remotePackets := messagingPacketPair()
	stack, err := usernet.Open(ctx, packets, usernet.Config{Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close(context.Background())
	remote, err := usernet.Open(ctx, remotePackets, usernet.Config{Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.2")}})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close(context.Background())
	peer, err := remote.ListenPacket(ctx, "udp4", "10.0.0.2:5000")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	rtcp, err := remote.ListenPacket(ctx, "udp4", "10.0.0.2:5001")
	if err != nil {
		t.Fatal(err)
	}
	defer rtcp.Close()
	controller, err := ims.NewIncomingCallController(stack, "10.0.0.1", "sip:mdd@10.0.0.1", "hold-local")
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(context.Background())
	ended := make(chan string, 2)
	controller.SetCarrierTerminator(holdTerminator{ended})
	const token = "0123456789abcdef0123456789abcdef"
	registry, err := browsermedia.NewRegistry(token, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.CloseAll()
	server := httptest.NewServer(registry)
	defer server.Close()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/media/held-call", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	writeHoldJSON(t, ctx, socket, map[string]any{"type": "browser.media.hello", "version": 1, "session_id": "held-call", "ticket": "fixture-ticket"})
	claimed := readHoldJSON(t, ctx, socket)
	if claimed["type"] != "browser.media.claimed" || claimed["connection_epoch"] != float64(1) {
		t.Fatalf("claim: %v", claimed)
	}
	if started := readHoldJSON(t, ctx, socket); started["purpose"] != "canary" {
		t.Fatalf("started: %v", started)
	}
	pcm := make([]byte, media.PCMFrameBytes)
	for i := 0; i < media.FrameSamples; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(i*101-6500)))
	}
	for range 2 {
		if err := socket.Write(ctx, websocket.MessageBinary, pcm); err != nil {
			t.Fatal(err)
		}
		if got := readHoldPCM(t, ctx, socket); !bytes.Equal(got, pcm) {
			t.Fatal("canary changed actual signal")
		}
	}
	evidence := map[string]any{"type": "browser.media.evidence", "version": 1, "challenge": claimed["challenge"],
		"capture_callbacks": 2, "playback_callbacks": 2, "played_frames": 2}
	writeHoldJSON(t, ctx, socket, evidence)
	if status := readHoldJSON(t, ctx, socket); status["ready"] != true {
		t.Fatalf("actual canary not ready: %v", status)
	}
	if ready := readHoldJSON(t, ctx, socket); ready["type"] != "browser.media.ready" {
		t.Fatalf("ready: %v", ready)
	}
	session, found := registry.Session("held-call")
	if !found || !session.Ready() {
		t.Fatal("missing proven media session")
	}
	runtime := &holdRuntime{&upstreamRuntime{inbound: &inboundMessaging{calls: controller}}}
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", cleanupFactory{runtime},
		NewMemoryOperationStore(), holdMediaDirectory{registry}, defaultCallGuardTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		if backend.activeCall != nil {
			backend.finishCallLocked(backend.activeCall)
		}
	}()
	if _, err := backend.Start(ctx, vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	request := voiceclient.SIPRequestMessage{Method: "INVITE", URI: "sip:mdd@ims.test", Headers: map[string]string{
		"Call-ID": "held-call", "CSeq": "1 INVITE", "From": "<sip:peer@ims.test>;tag=remote", "To": "<sip:mdd@ims.test>",
	}, Body: []byte("v=0\r\no=test 1 1 IN IP4 10.0.0.2\r\ns=hold\r\nc=IN IP4 10.0.0.2\r\nt=0 0\r\nm=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=rtcp:5001\r\na=sendrecv\r\na=ptime:20\r\n")}
	ringing, final := make(chan struct{}), make(chan voiceclient.SIPResponse, 1)
	go func() {
		answer, _ := controller.RoundTripInvite(ctx, request, func(context.Context, voiceclient.SIPRequestMessage, voiceclient.SIPResponse) error {
			close(ringing)
			return nil
		})
		final <- answer
	}()
	select {
	case <-ringing:
	case <-ctx.Done():
		t.Fatal("incoming call did not ring")
	}
	result, err := backend.AnswerIncomingCall(ctx, vowifiipc.AnswerIncomingCallRequest{OperationID: "answer", CallID: "held-call", MediaBufferMS: 500})
	if err != nil || result.Code != "active" {
		t.Fatalf("answer %+v: %v", result, err)
	}
	answer := <-final
	if answer.StatusCode != 200 {
		t.Fatalf("SIP answer %d", answer.StatusCode)
	}
	request.Headers["To"] = answer.Headers["To"][0]
	backend.mu.Lock()
	active := backend.activeCall
	backend.mu.Unlock()
	// This is the unchanged client's liveness contract, not an Android emulator:
	// real client evidence keeps the guard alive, and every read has a 5s limit.
	heartbeatContext, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		payload, _ := json.Marshal(evidence)
		for {
			select {
			case <-heartbeatContext.Done():
				return
			case <-ticker.C:
				if socket.Write(ctx, websocket.MessageText, payload) != nil {
					return
				}
			}
		}
	}()
	exchangeHoldAudio(t, ctx, socket, peer, pcm, 1)
	reinvite := func(direction string, sequence int) {
		t.Helper()
		update := request
		update.Headers = map[string]string{"Call-ID": "held-call", "CSeq": fmt.Sprintf("%d INVITE", sequence), "From": request.Headers["From"], "To": request.Headers["To"]}
		update.Body = bytes.ReplaceAll(request.Body, []byte("a=sendrecv"), []byte("a="+direction))
		update.Body = bytes.Replace(update.Body, []byte("o=test 1 1 "), []byte(fmt.Sprintf("o=test 1 %d ", sequence)), 1)
		response, err := controller.RoundTripInvite(ctx, update, nil)
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("%s rejected: %d %v", direction, response.StatusCode, err)
		}
	}
	reinvite("inactive", 2)
	start, frames := time.Now(), 0
	for time.Since(start) < 16*time.Second {
		if got := readHoldPCM(t, ctx, socket); !bytes.Equal(got, make([]byte, media.PCMFrameBytes)) {
			t.Fatal("held downlink was not silence")
		}
		frames++
		select {
		case <-active.done:
			t.Fatal("call guard hung up a held, connected client")
		default:
		}
	}
	if frames < 500 || time.Since(session.LastSeen()) > time.Second {
		t.Fatalf("held continuity: frames=%d client age=%v", frames, time.Since(session.LastSeen()))
	}
	if current, ok := registry.Session("held-call"); !ok || current != session || !session.Connected() {
		t.Fatal("held call replaced its WebSocket session")
	}
	reinvite("sendrecv", 3)
	for i := 0; i < media.FrameSamples; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(i*73-5000)))
	}
	exchangeHoldAudio(t, ctx, socket, peer, pcm, 2)
	reinvite("inactive", 4)
	stopHeartbeat()
	<-heartbeatDone
	// Continue reading generated silence, but send nothing at all. Padding must
	// not refresh the client's timestamp or defeat the exact-call timeout.
	readHoldPCM(t, ctx, socket)
	lastSeen := session.LastSeen()
	for {
		kind, _, err := socket.Read(ctx)
		if err != nil {
			// EndStream cancels the reader before closing, so EOF is valid.
			// The exact call receipt below, not a close code, proves termination.
			if ctx.Err() != nil || time.Since(lastSeen) < defaultCallGuardTimeout {
				t.Fatalf("connection ended outside the guard deadline: %v", err)
			}
			break
		}
		if kind != websocket.MessageBinary || session.LastSeen() != lastSeen {
			t.Fatal("padding forged client liveness")
		}
	}
	select {
	case id := <-ended:
		if id != "held-call" {
			t.Fatalf("guard ended wrong call: %s", id)
		}
	default:
		t.Fatal("guard did not terminate the original call")
	}
	select {
	case <-active.done:
	default:
		t.Fatal("guard retained the ended active call")
	}
}

func writeHoldJSON(t *testing.T, ctx context.Context, socket *websocket.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func readHoldJSON(t *testing.T, ctx context.Context, socket *websocket.Conn) map[string]any {
	t.Helper()
	kind, data, err := socket.Read(ctx)
	var value map[string]any
	if err != nil || kind != websocket.MessageText || json.Unmarshal(data, &value) != nil {
		t.Fatalf("expected control message: %v", err)
	}
	return value
}

func readHoldPCM(t *testing.T, ctx context.Context, socket *websocket.Conn) []byte {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	kind, data, err := socket.Read(deadline)
	if err != nil || kind != websocket.MessageBinary || len(data) != media.PCMFrameBytes {
		t.Fatalf("downlink exceeded Android silence deadline or changed frame: %v", err)
	}
	return data
}

func exchangeHoldAudio(t *testing.T, ctx context.Context, socket *websocket.Conn, peer net.PacketConn, pcm []byte, sequence uint16) {
	t.Helper()
	if err := socket.Write(ctx, websocket.MessageBinary, pcm); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	wire := make([]byte, 2048)
	var packet rtp.Packet
	var source net.Addr
	for {
		n, address, err := peer.ReadFrom(wire)
		if err != nil || packet.Unmarshal(wire[:n]) != nil {
			t.Fatalf("actual uplink RTP missing: %v", err)
		}
		if packet.PayloadType == 0 && bytes.Equal(packet.Payload, g711.EncodeUlaw(pcm)) {
			source = address
			break
		}
	}
	packet.Header = rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: sequence, Timestamp: uint32(sequence) * 160, SSRC: 91}
	encoded, err := packet.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.WriteTo(encoded, source); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		got := readHoldPCM(t, ctx, socket)
		if bytes.Equal(got, g711.DecodeUlaw(packet.Payload)) {
			return
		}
		if !bytes.Equal(got, make([]byte, media.PCMFrameBytes)) {
			t.Fatal("actual resumed downlink content changed")
		}
	}
	t.Fatal("silence padding masked resumed carrier audio")
}
