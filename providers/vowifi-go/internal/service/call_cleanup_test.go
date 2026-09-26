// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

// Adapted from PR #9's d005080 cleanup counterexample, without its separate
// durable-receipt API. SIP/dialog, media, runtime wrapper and Backend are real.
type cleanupPacketSink struct{}

func (cleanupPacketSink) Send(context.Context, []byte) error { return nil }
func (cleanupPacketSink) Receive(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (cleanupPacketSink) Close(context.Context) error { return nil }

type cleanupSIPPeer struct {
	mu            sync.Mutex
	ackFailure    bool
	invalidSDP    bool
	byes, invites int
	allowCleanup  chan struct{}
}

func (peer *cleanupSIPPeer) RoundTripInvite(ctx context.Context, request voiceclient.SIPRequestMessage, _ voiceclient.ProvisionalResponseHandler) (voiceclient.SIPResponse, error) {
	request.Headers["Via"] = "SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-cleanup;rport"
	return peer.RoundTripRequest(ctx, request)
}

func (peer *cleanupSIPPeer) RoundTripRequest(ctx context.Context, request voiceclient.SIPRequestMessage) (voiceclient.SIPResponse, error) {
	peer.mu.Lock()
	switch request.Method {
	case "INVITE":
		peer.invites++
		peer.mu.Unlock()
		body := "v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 5000 RTP/AVP 96\r\na=rtpmap:96 AMR/8000\r\na=ptime:30\r\n"
		if peer.invalidSDP {
			body = "v=0\r\nm=audio invalid RTP/AVP 96\r\n"
		}
		return voiceclient.SIPResponse{StatusCode: 200, Reason: "synthetic peer", Headers: map[string][]string{
			"To": {"<sip:peer@ims.test>;tag=peer-tag"}, "Contact": {"<sip:peer@192.0.2.20:5060>"},
		}, Body: []byte(body)}, nil
	case "BYE":
		peer.byes++
		count := peer.byes
		peer.mu.Unlock()
		if count <= 2 {
			return voiceclient.SIPResponse{StatusCode: 503, Reason: "synthetic not ended"}, nil
		}
		select {
		case <-peer.allowCleanup:
		case <-ctx.Done():
			return voiceclient.SIPResponse{}, ctx.Err()
		}
		return voiceclient.SIPResponse{StatusCode: 200, Reason: "OK"}, nil
	default:
		peer.mu.Unlock()
		return voiceclient.SIPResponse{}, errors.New("unexpected synthetic SIP method")
	}
}

func (peer *cleanupSIPPeer) WriteRequest(_ context.Context, request voiceclient.SIPRequestMessage) error {
	if request.Method == "ACK" && peer.ackFailure {
		return errors.New("synthetic ACK write loss")
	}
	return nil
}

type cleanupRuntime struct {
	*fakeRuntime
	actual   *upstreamRuntime
	call     VoiceCall
	startErr error
}

func (runtime *cleanupRuntime) StartMediaCall(ctx context.Context, request vowifiipc.StartCallRequest) (VoiceCall, error) {
	if runtime.actual != nil {
		return runtime.actual.StartMediaCall(ctx, request)
	}
	return runtime.call, runtime.startErr
}

type cleanupFactory struct{ Runtime }

func (factory cleanupFactory) Start(context.Context) (Runtime, error) { return factory.Runtime, nil }

func newCleanupBackend(t *testing.T, runtime Runtime) *Backend {
	t.Helper()
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", cleanupFactory{runtime},
		NewMemoryOperationStore(), fakeMediaDirectory{newFakeMediaSession()}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		if backend.activeCall != nil {
			backend.finishCallLocked(backend.activeCall)
		}
	})
	return backend
}

func TestCallCleanupRetainsEstablishedDialogAcrossWrappers(t *testing.T) {
	for _, scenario := range []string{"unsupported_media", "invalid_sdp", "ack_loss"} {
		t.Run(scenario, func(t *testing.T) {
			stack, err := usernet.Open(t.Context(), cleanupPacketSink{}, usernet.Config{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.10")}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stack.Close(context.Background()) })
			peer := &cleanupSIPPeer{ackFailure: scenario == "ack_loss", invalidSDP: scenario == "invalid_sdp", allowCleanup: make(chan struct{})}
			var release sync.Once
			unblock := func() { release.Do(func() { close(peer.allowCleanup) }) }
			t.Cleanup(unblock)
			runtime := &cleanupRuntime{fakeRuntime: &fakeRuntime{}, actual: &upstreamRuntime{
				stack: stack, localIP: "192.0.2.10", deviceID: "cleanup-device",
				registration: runtimehost.IMSRegistrationResult{Registered: true, VoiceTransport: peer,
					Profile: voiceclient.IMSProfile{IMPU: "sip:self@ims.test", Domain: "ims.test"},
					Binding: voiceclient.RegistrationBinding{ContactURI: "sip:self@192.0.2.10:5060", PublicIdentity: "sip:self@ims.test"}},
			}}
			backend := newCleanupBackend(t, runtime)
			if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "original-start", CallID: "call-1", Callee: "+100", MediaBufferMS: 500}); err == nil {
				t.Fatal("invalid media unexpectedly started")
			}
			backend.mu.Lock()
			active := backend.activeCall
			retained := active != nil && active.call != nil && active.cleanupPending && !active.terminationConfirmed
			backend.mu.Unlock()
			if !retained {
				t.Fatal("established dialog lost cleanup ownership through a wrapper")
			}
			if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "other-start", CallID: "call-2", Callee: "+200", MediaBufferMS: 500}); err == nil {
				t.Fatal("new call bypassed unresolved cleanup")
			}
			unblock()
			select {
			case <-active.done:
			case <-time.After(3 * time.Second):
				t.Fatal("original call cleanup did not finish")
			}
			peer.mu.Lock()
			defer peer.mu.Unlock()
			if peer.invites != 1 || peer.byes != 3 {
				t.Fatalf("SIP actions: INVITE=%d BYE=%d, want 1/3", peer.invites, peer.byes)
			}
		})
	}
}

type cleanupUnacceptedCall struct{ *fakeVoiceCall }

func (call *cleanupUnacceptedCall) End(context.Context) (voicehost.DialogInfoResult, error) {
	call.ends.Add(1)
	return voicehost.DialogInfoResult{Accepted: false, StatusCode: 503}, nil
}

func TestCallCleanupRequiresPositiveEndConfirmation(t *testing.T) {
	for _, action := range []string{"manual_end", "runtime_stop", "failed_start"} {
		t.Run(action, func(t *testing.T) {
			call := &cleanupUnacceptedCall{newFakeVoiceCall()}
			runtime := &cleanupRuntime{fakeRuntime: &fakeRuntime{}, call: call}
			if action == "failed_start" {
				runtime.startErr = errors.New("synthetic media start failure")
			}
			backend := newCleanupBackend(t, runtime)
			_, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "original-start", CallID: "call-1", Callee: "+100", MediaBufferMS: 500})
			if action != "failed_start" && err != nil {
				t.Fatal(err)
			}
			switch action {
			case "manual_end":
				_, err = backend.EndCall(t.Context(), vowifiipc.EndCallRequest{OperationID: "original-end", CallID: "call-1"})
			case "runtime_stop":
				_, err = backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-stop"})
			}
			backend.mu.Lock()
			retained := backend.activeCall != nil && backend.activeCall.call == call && !backend.activeCall.terminationConfirmed
			backend.mu.Unlock()
			if err == nil || !retained || runtime.closes.Load() != 0 {
				t.Fatalf("unaccepted End lost cleanup ownership: err=%v retained=%t closes=%d", err, retained, runtime.closes.Load())
			}
		})
	}
}
