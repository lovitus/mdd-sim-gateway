// SPDX-License-Identifier: AGPL-3.0-only
package service

import (
	"context"
	"errors"
	"io"
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

// The real Backend -> upstreamRuntime -> IMS agent/bridge chain, with only the
// carrier transport replaced. No host route, SIM, phone number or paid endpoint.
type pr8Packets struct {
	closed chan struct{}
	once   sync.Once
}

func (p *pr8Packets) Send(context.Context, []byte) error { return nil }
func (p *pr8Packets) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.closed:
		return nil, io.EOF
	}
}
func (p *pr8Packets) Close(context.Context) error { p.once.Do(func() { close(p.closed) }); return nil }

type pr8SIP struct {
	reject        bool
	badMedia      bool
	rejectEnd     atomic.Bool
	invites, byes atomic.Int32
}

func (s *pr8SIP) RoundTripRequest(_ context.Context, q voiceclient.SIPRequestMessage) (voiceclient.SIPResponse, error) {
	switch q.Method {
	case "INVITE":
		s.invites.Add(1)
		if s.reject {
			return voiceclient.SIPResponse{StatusCode: 486, Reason: "Busy Here", Headers: map[string][]string{"To": {"<sip:peer@ims.test>;tag=remote"}}}, nil
		}
		ptime := "20"
		if s.badMedia {
			ptime = "30"
		}
		return voiceclient.SIPResponse{StatusCode: 200, Reason: "OK", Headers: map[string][]string{"To": {"<sip:peer@ims.test>;tag=remote"}, "Contact": {"<sip:peer@192.0.2.2:5060>"}}, Body: []byte("v=0\r\nc=IN IP4 192.0.2.2\r\nm=audio 5000 RTP/AVP 96\r\na=rtpmap:96 AMR/8000\r\na=ptime:" + ptime + "\r\n")}, nil
	case "BYE":
		s.byes.Add(1)
		if s.rejectEnd.Load() {
			return voiceclient.SIPResponse{StatusCode: 503, Reason: "Unavailable"}, nil
		}
		return voiceclient.SIPResponse{StatusCode: 200, Reason: "OK"}, nil
	default:
		return voiceclient.SIPResponse{}, errors.New("unexpected synthetic SIP request")
	}
}
func (*pr8SIP) WriteRequest(_ context.Context, q voiceclient.SIPRequestMessage) error {
	if q.Method != "ACK" {
		return errors.New("unexpected synthetic SIP write")
	}
	return nil
}

type pr8Runtime struct {
	*fakeRuntime
	real *upstreamRuntime
}

func (r *pr8Runtime) StartMediaCall(ctx context.Context, q vowifiipc.StartCallRequest) (VoiceCall, error) {
	return r.real.StartMediaCall(ctx, q)
}
func pr8Backend(t *testing.T, carrier *pr8SIP, store OperationStore) (*Backend, *pr8Runtime) {
	t.Helper()
	packets := &pr8Packets{closed: make(chan struct{})}
	stack, err := usernet.Open(t.Context(), packets, usernet.Config{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.1")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := stack.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	run := &pr8Runtime{fakeRuntime: &fakeRuntime{}, real: &upstreamRuntime{stack: stack, localIP: "192.0.2.1", deviceID: "synthetic", registration: runtimehost.IMSRegistrationResult{Registered: true, VoiceTransport: carrier, Profile: voiceclient.IMSProfile{IMPU: "sip:user@ims.test", Domain: "ims.test"}, Binding: voiceclient.RegistrationBinding{ContactURI: "sip:user@192.0.2.1:5060", PublicIdentity: "sip:user@ims.test"}}}}
	session := newFakeMediaSession()
	session.setConnected(true, time.Now().Add(time.Hour))
	backend, err := NewBackendWithMediaStore("line-1", "native", "process-1", reviewFactory{runtime: run}, store, fakeMediaDirectory{session: session}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		carrier.rejectEnd.Store(false)
		_, _ = backend.EndCall(context.Background(), vowifiipc.EndCallRequest{OperationID: "test-final-cleanup", CallID: "call-1", ReasonCode: "test_cleanup"})
	})
	return backend, run
}
func pr8Start() vowifiipc.StartCallRequest {
	return vowifiipc.StartCallRequest{OperationID: "original-start", CallID: "call-1", MediaSessionID: "call-1", Callee: "+100", MediaBufferMS: 500}
}
func pr8Query() vowifiipc.CallReceiptRequest {
	return vowifiipc.CallReceiptRequest{CallID: "call-1", OperationID: "original-start", SessionID: "call-1"}
}

func TestPR8AcceptedMediaFailureRetainsExactCleanupAcrossAllLayers(t *testing.T) {
	carrier := &pr8SIP{badMedia: true}
	carrier.rejectEnd.Store(true)
	backend, _ := pr8Backend(t, carrier, NewMemoryOperationStore())
	if _, err := backend.StartCall(t.Context(), pr8Start()); err == nil {
		t.Fatal("media failure hidden")
	}
	backend.mu.Lock()
	active := backend.activeCall
	retained := active != nil && active.call != nil && active.cleanupPending && active.guardCancel != nil
	backend.mu.Unlock()
	if !retained {
		t.Fatal("accepted dialog lost its callable cleanup owner")
	}
	if _, err := backend.CallReceipt(t.Context(), pr8Query()); err == nil {
		t.Fatal("unconfirmed BYE became terminal")
	}
	carrier.rejectEnd.Store(false)
	if _, err := backend.EndCall(t.Context(), vowifiipc.EndCallRequest{OperationID: "explicit-end", CallID: "call-1", ReasonCode: "user_recovery_hangup", ExpectedStartOperationID: "original-start"}); err != nil {
		t.Fatal(err)
	}
	if proof, err := backend.CallReceipt(t.Context(), pr8Query()); err != nil || proof.Source != "confirmed_end" {
		t.Fatal(proof, err)
	}
	if carrier.invites.Load() != 1 {
		t.Fatal("cleanup redialed original call")
	}
}

func TestPR8RejectedCallHasDurableOutcomeWithoutAnotherInvite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	carrier := &pr8SIP{reject: true}
	backend, _ := pr8Backend(t, carrier, store)
	if _, err = backend.StartCall(t.Context(), pr8Start()); err == nil {
		t.Fatal("rejection hidden")
	}
	proof, err := backend.CallReceipt(t.Context(), pr8Query())
	if err != nil || proof.Source != "confirmed_rejected" {
		t.Fatalf("final rejection has no recoverable exact outcome: %+v %v", proof, err)
	}
	if _, err = backend.StartCall(t.Context(), pr8Start()); err == nil {
		t.Fatal("rejected operation replay became success")
	}
	if carrier.invites.Load() != 1 || carrier.byes.Load() != 0 {
		t.Fatal("rejection replay dispatched SIP again")
	}
	// No active work may reference the closed DB during the simulated restart.
	backend.mu.Lock()
	empty := backend.activeCall == nil
	backend.mu.Unlock()
	if !empty {
		t.Fatal("rejection retained busy owner")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fresh, err := NewBackendWithMediaStore("line-1", "native", "process-2", &fakeFactory{run: &fakeRuntime{}}, reopened, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := fresh.CallReceipt(t.Context(), pr8Query())
	if err != nil || restored.Source != proof.Source || !restored.ConfirmedAt.Equal(proof.ConfirmedAt) || restored.ProcessGeneration != "process-1" {
		t.Fatal(restored, err)
	}
	// Cleanup fixture must no longer call the closed original store.
	backend.operations = NewMemoryOperationStore()
}

func TestPR8StopPersistsCallTerminalEvenWhenRuntimeCloseFails(t *testing.T) {
	for _, closeFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "close-fails"}[closeFailure], func(t *testing.T) {
			carrier := &pr8SIP{}
			backend, run := pr8Backend(t, carrier, NewMemoryOperationStore())
			if closeFailure {
				run.closeErr = errors.New("synthetic close failure")
			}
			if _, err := backend.StartCall(t.Context(), pr8Start()); err != nil {
				t.Fatal(err)
			}
			_, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "stop"})
			if (err != nil) != closeFailure {
				t.Fatal(err)
			}
			proof, err := backend.CallReceipt(t.Context(), pr8Query())
			if err != nil || proof.Source != "confirmed_end" {
				t.Fatalf("Stop discarded exact terminal evidence: %+v %v", proof, err)
			}
			if carrier.invites.Load() != 1 || carrier.byes.Load() != 1 {
				t.Fatal("receipt lookup repeated a SIP side effect")
			}
		})
	}
}

type pr8ReceiptFault struct {
	OperationStore
	fail atomic.Bool
}

func (s *pr8ReceiptFault) SaveCallReceipt(scope string, r vowifiipc.CallReceipt) error {
	if s.fail.Load() {
		return errors.New("injected terminal write failure")
	}
	return s.OperationStore.SaveCallReceipt(scope, r)
}
func TestPR8StopRetainsReceiptRetryWithoutRepeatingConfirmedBye(t *testing.T) {
	carrier := &pr8SIP{}
	store := &pr8ReceiptFault{OperationStore: NewMemoryOperationStore()}
	backend, run := pr8Backend(t, carrier, store)
	if _, err := backend.StartCall(t.Context(), pr8Start()); err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "stop"}); err == nil {
		t.Fatal("failed receipt write hidden")
	}
	backend.mu.Lock()
	active := backend.activeCall
	retained := active != nil && active.terminationConfirmed && active.guardCancel != nil
	backend.mu.Unlock()
	if !retained || run.closes.Load() != 0 {
		t.Fatal("terminal persistence failure lost retry ownership")
	}
	store.fail.Store(false)
	if _, err := backend.CallReceipt(t.Context(), pr8Query()); err != nil {
		t.Fatal(err)
	}
	if carrier.byes.Load() != 1 {
		t.Fatal("persist retry repeated confirmed BYE")
	}
}

type pr8UnacceptedCall struct{ *fakeVoiceCall }

func (c *pr8UnacceptedCall) End(context.Context) (voicehost.DialogInfoResult, error) {
	c.ends.Add(1)
	return voicehost.DialogInfoResult{Accepted: false}, nil
}
func TestPR8StopRequiresAcceptedTerminalResult(t *testing.T) {
	call := &pr8UnacceptedCall{newFakeVoiceCall()}
	run := &reviewVoiceRuntime{fakeRuntime: &fakeRuntime{}, calls: map[string]VoiceCall{"call-1": call}}
	b, err := NewBackendWithMediaStore("line-1", "native", "process", reviewFactory{runtime: run}, NewMemoryOperationStore(), fakeMediaDirectory{session: newFakeMediaSession()}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.StartCall(t.Context(), pr8Start()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		b.mu.Lock()
		if b.activeCall != nil && b.activeCall.guardCancel != nil {
			b.activeCall.guardCancel()
		}
		b.mu.Unlock()
	}()
	if _, err = b.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "stop"}); err == nil {
		t.Fatal("Stop accepted an unconfirmed terminal response")
	}
	if run.closes.Load() != 0 {
		t.Fatal("runtime closed before terminal confirmation")
	}
}
