// SPDX-License-Identifier: AGPL-3.0-only
package service

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

// The real upstream SIP/dialog implementation, IMS media adapter, runtime wrapper
// and Provider Backend run together. Only the registered SIP peer/inner packet
// transport and browser media are synthetic; there is no carrier or host route.
type repairPacketSink struct{}

func (repairPacketSink) Send(context.Context, []byte) error { return nil }
func (repairPacketSink) Receive(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (repairPacketSink) Close(context.Context) error { return nil }

type repairSIPPeer struct {
	mu            sync.Mutex
	status        int
	ackFailure    bool
	byes, invites int
	allowCleanup  chan struct{}
}

func (p *repairSIPPeer) RoundTripInvite(ctx context.Context, q voiceclient.SIPRequestMessage, _ voiceclient.ProvisionalResponseHandler) (voiceclient.SIPResponse, error) {
	q.Headers["Via"] = "SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-repair;rport"
	return p.RoundTripRequest(ctx, q)
}
func (p *repairSIPPeer) RoundTripRequest(ctx context.Context, q voiceclient.SIPRequestMessage) (voiceclient.SIPResponse, error) {
	p.mu.Lock()
	switch q.Method {
	case "INVITE":
		p.invites++
		status := p.status
		p.mu.Unlock()
		return voiceclient.SIPResponse{StatusCode: status, Reason: "synthetic peer", Headers: map[string][]string{"To": {"<sip:peer@ims.test>;tag=peer-tag"}, "Contact": {"<sip:peer@192.0.2.20:5060>"}}, Body: []byte("v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 5000 RTP/AVP 96\r\na=rtpmap:96 AMR/8000\r\na=ptime:30\r\n")}, nil
	case "BYE":
		p.byes++
		count := p.byes
		gate := p.allowCleanup
		p.mu.Unlock()
		if gate != nil && count <= 2 {
			return voiceclient.SIPResponse{StatusCode: 503, Reason: "synthetic not ended"}, nil
		}
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return voiceclient.SIPResponse{}, ctx.Err()
			}
		}
		return voiceclient.SIPResponse{StatusCode: 200, Reason: "OK"}, nil
	default:
		p.mu.Unlock()
		return voiceclient.SIPResponse{}, errors.New("unexpected synthetic SIP method")
	}
}
func (p *repairSIPPeer) WriteRequest(_ context.Context, q voiceclient.SIPRequestMessage) error {
	if q.Method == "ACK" && p.ackFailure {
		return errors.New("synthetic ACK write loss")
	}
	return nil
}
func (p *repairSIPPeer) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.invites, p.byes
}

type repairNativeRuntime struct {
	*fakeRuntime
	actual *upstreamRuntime
}

func (r *repairNativeRuntime) StartMediaCall(ctx context.Context, q vowifiipc.StartCallRequest) (VoiceCall, error) {
	return r.actual.StartMediaCall(ctx, q)
}
func repairBackend(t *testing.T, peer *repairSIPPeer, store OperationStore) *Backend {
	t.Helper()
	stack, err := usernet.Open(context.Background(), repairPacketSink{}, usernet.Config{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.10")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = stack.Close(ctx)
	})
	runtime := &repairNativeRuntime{fakeRuntime: &fakeRuntime{}, actual: &upstreamRuntime{stack: stack, localIP: "192.0.2.10", deviceID: "repair-device", registration: runtimehost.IMSRegistrationResult{
		Registered: true, VoiceTransport: peer, Profile: voiceclient.IMSProfile{IMPU: "sip:self@ims.test", Domain: "ims.test"}, Binding: voiceclient.RegistrationBinding{ContactURI: "sip:self@192.0.2.10:5060", PublicIdentity: "sip:self@ims.test"},
	}}}
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", reviewFactory{runtime}, store, fakeMediaDirectory{session: newFakeMediaSession()}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	return backend
}
func repairStart() vowifiipc.StartCallRequest {
	return vowifiipc.StartCallRequest{OperationID: "original-start", CallID: "call-1", Callee: "+100", MediaBufferMS: 500}
}
func repairQuery() vowifiipc.CallReceiptRequest {
	return vowifiipc.CallReceiptRequest{OperationID: "original-start", CallID: "call-1", SessionID: "call-1"}
}

func TestReviewFixEstablishedMediaFailureRetainsExactCleanupAcrossAllWrappers(t *testing.T) {
	for _, ackFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported_media", true: "ACK_loss"}[ackFailure], func(t *testing.T) {
			peer := &repairSIPPeer{status: 200, ackFailure: ackFailure, allowCleanup: make(chan struct{})}
			var release sync.Once
			unblock := func() { release.Do(func() { close(peer.allowCleanup) }) }
			t.Cleanup(unblock)
			backend := repairBackend(t, peer, NewMemoryOperationStore())
			if _, err := backend.StartCall(t.Context(), repairStart()); err == nil {
				t.Fatal("invalid media unexpectedly started")
			}
			backend.mu.Lock()
			retained := backend.activeCall != nil && backend.activeCall.call != nil && backend.activeCall.cleanupPending
			backend.mu.Unlock()
			if !retained {
				t.Fatal("established dialog lost cleanup ownership through a nil wrapper")
			}
			if _, err := backend.CallReceipt(t.Context(), repairQuery()); err == nil {
				t.Fatal("failed BYE invented terminal proof")
			}
			if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{OperationID: "other-start", CallID: "call-2", Callee: "+200", MediaBufferMS: 500}); err == nil {
				t.Fatal("new call bypassed unresolved cleanup")
			}
			unblock()
			reviewAwait(t, func() bool { backend.mu.Lock(); defer backend.mu.Unlock(); return backend.activeCall == nil })
			proof, err := backend.CallReceipt(t.Context(), repairQuery())
			if err != nil || proof.Source != "confirmed_end" {
				t.Fatal(proof, err)
			}
			invites, byes := peer.counts()
			if invites != 1 || byes != 3 {
				t.Fatalf("SIP actions: INVITE=%d BYE=%d, want 1/3", invites, byes)
			}
		})
	}
}

func TestReviewFixFinalCarrierRejectionIsDurableAndNotRedialed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	peer := &repairSIPPeer{status: 486}
	backend := repairBackend(t, peer, store)
	if _, err = backend.StartCall(t.Context(), repairStart()); err == nil {
		t.Fatal("rejection became success")
	}
	proof, err := backend.CallReceipt(t.Context(), repairQuery())
	if err != nil || proof.Source != "carrier_rejected" {
		t.Fatalf("rejection has no precise outcome: %+v %v", proof, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restarted, err := NewBackendWithMediaStore("line-1", "native", "process-2", reviewFactory{&fakeRuntime{}}, store, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proof, err = restarted.CallReceipt(t.Context(), repairQuery())
	if err != nil || proof.ProcessGeneration != "process-1" || proof.Source != "carrier_rejected" {
		t.Fatal(proof, err)
	}
	bad := repairQuery()
	bad.OperationID = "other-operation"
	if _, err = restarted.CallReceipt(t.Context(), bad); err == nil {
		t.Fatal("wrong operation obtained proof")
	}
	invites, byes := peer.counts()
	if invites != 1 || byes != 0 {
		t.Fatalf("rejection SIP actions=%d/%d", invites, byes)
	}
}

type repairReceiptStore struct {
	OperationStore
	fail atomic.Bool
}

func (s *repairReceiptStore) SaveCallReceipt(scope string, proof vowifiipc.CallReceipt) error {
	if s.fail.Load() {
		return errors.New("injected terminal persistence failure")
	}
	return s.OperationStore.SaveCallReceipt(scope, proof)
}

func TestReviewFixRejectedReceiptPersistenceCanRecoverWithoutAnotherCall(t *testing.T) {
	store := &repairReceiptStore{OperationStore: NewMemoryOperationStore()}
	store.fail.Store(true)
	peer := &repairSIPPeer{status: 486}
	backend := repairBackend(t, peer, store)
	_, _ = backend.StartCall(t.Context(), repairStart())
	backend.mu.Lock()
	retained := backend.activeCall != nil
	backend.mu.Unlock()
	if !retained {
		t.Fatal("failed final proof was discarded")
	}
	if _, err := backend.CallReceipt(t.Context(), repairQuery()); err == nil {
		t.Fatal("failed proof storage became success")
	}
	store.fail.Store(false)
	proof, err := backend.CallReceipt(t.Context(), repairQuery())
	if err != nil || proof.Source != "carrier_rejected" {
		t.Fatal(proof, err)
	}
	invites, byes := peer.counts()
	if invites != 1 || byes != 0 {
		t.Fatalf("recovery repeated business actions=%d/%d", invites, byes)
	}
}

func TestReviewFixStopPersistsExactTerminalBeforeRuntimeClose(t *testing.T) {
	for _, closeFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "deregister_failure"}[closeFailure], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operations.db")
			store, err := OpenBoltOperationStore(path)
			if err != nil {
				t.Fatal(err)
			}
			call := newFakeVoiceCall()
			runtime := &fakeRuntime{call: call}
			if closeFailure {
				runtime.closeErr = errors.New("synthetic deregister failure")
			}
			backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", &fakeFactory{run: runtime}, store, fakeMediaDirectory{newFakeMediaSession()}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			_, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = backend.StartCall(t.Context(), repairStart())
			if err != nil {
				t.Fatal(err)
			}
			_, err = backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-stop"})
			if (err != nil) != closeFailure {
				t.Fatal(err)
			}
			if runtime.closes.Load() != 1 || call.ends.Load() != 1 {
				t.Fatal("unexpected stop actions")
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenBoltOperationStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			restarted, err := NewBackendWithMediaStore("line-1", "native", "process-2", &fakeFactory{run: &fakeRuntime{}}, store, nil, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := restarted.CallReceipt(t.Context(), repairQuery())
			if err != nil || proof.Source != "confirmed_end" || proof.ProcessGeneration != "process-1" {
				t.Fatal(proof, err)
			}
		})
	}
}
func TestReviewFixStopPersistenceFailureRetainsProofAndDoesNotRepeatBYE(t *testing.T) {
	store := &repairReceiptStore{OperationStore: NewMemoryOperationStore()}
	call := newFakeVoiceCall()
	runtime := &fakeRuntime{call: call}
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", &fakeFactory{run: runtime}, store, fakeMediaDirectory{newFakeMediaSession()}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.StartCall(t.Context(), repairStart())
	if err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	if _, err = backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-stop"}); err == nil {
		t.Fatal("lost terminal persistence accepted")
	}
	if runtime.closes.Load() != 0 || call.ends.Load() != 1 {
		t.Fatal("destroyed runtime before exact proof")
	}
	store.fail.Store(false)
	proof, err := backend.CallReceipt(t.Context(), repairQuery())
	if err != nil || proof.Source != "confirmed_end" {
		t.Fatal(proof, err)
	}
	if call.ends.Load() != 1 {
		t.Fatal("persistence retry repeated BYE")
	}
}

// A no-error return from a backend is not necessarily confirmation.
type repairRejectedEnd struct{ *fakeVoiceCall }

func (c *repairRejectedEnd) End(context.Context) (voicehost.DialogInfoResult, error) {
	c.ends.Add(1)
	return voicehost.DialogInfoResult{Accepted: false, StatusCode: 503}, nil
}
func TestReviewFixStopDoesNotTreatUnacceptedResultAsTerminal(t *testing.T) {
	call := &repairRejectedEnd{newFakeVoiceCall()}
	runtime := &reviewVoiceRuntime{fakeRuntime: &fakeRuntime{}, calls: map[string]VoiceCall{"call-1": call}}
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", reviewFactory{runtime}, NewMemoryOperationStore(), fakeMediaDirectory{newFakeMediaSession()}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.StartCall(t.Context(), repairStart())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		if backend.activeCall != nil {
			backend.finishCallLocked(backend.activeCall)
		}
	})
	if _, err = backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-stop"}); err == nil {
		t.Fatal("unaccepted End became terminal")
	}
	if runtime.closes.Load() != 0 {
		t.Fatal("runtime destroyed without confirmation")
	}
	if _, err = backend.CallReceipt(t.Context(), repairQuery()); err == nil {
		t.Fatal("unaccepted End minted receipt")
	}
}
