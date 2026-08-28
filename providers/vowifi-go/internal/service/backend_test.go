// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/browsermedia"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
)

func TestBackendDurableDrainBlocksNewPaidOperationsUntilExactResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{}
	backend, err := NewBackendWithStore("line-1", "native", "process-1", &fakeFactory{run: runtime}, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	drained, err := backend.BeginDrain(t.Context(), vowifiipc.MaintenanceRequest{LeaseID: "apply-lease-1"})
	if err != nil || !drained.Draining || !drained.Status.Maintenance.Draining {
		t.Fatalf("drained=%+v err=%v", drained, err)
	}
	if _, err := backend.SendMessage(t.Context(), vowifiipc.SendMessageRequest{
		OperationID: "send-after-drain", MessageID: "message-1", Recipient: "+100", Body: "blocked",
	}); operationCode(err) != "apply_drain_active" || runtime.messages.Load() != 0 {
		t.Fatalf("send after drain count=%d err=%v", runtime.messages.Load(), err)
	}
	if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{
		OperationID: "call-after-drain", CallID: "call-1", Callee: "+100", MediaBufferMS: 500,
	}); operationCode(err) != "apply_drain_active" || runtime.callStarts.Load() != 0 {
		t.Fatalf("call after drain starts=%d err=%v", runtime.callStarts.Load(), err)
	}
	if _, err := backend.EndDrain(t.Context(), vowifiipc.MaintenanceRequest{LeaseID: "wrong-lease"}); operationCode(err) != "maintenance_lease_mismatch" {
		t.Fatalf("wrong resume err=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend, err = NewBackendWithStore("line-1", "native", "process-2", &fakeFactory{run: &fakeRuntime{}}, store)
	if err != nil {
		t.Fatal(err)
	}
	status, err := backend.Status(t.Context())
	if err != nil || !status.Maintenance.Draining {
		t.Fatalf("reopened status=%+v err=%v", status, err)
	}
	resumed, err := backend.EndDrain(t.Context(), vowifiipc.MaintenanceRequest{LeaseID: "apply-lease-1"})
	if err != nil || resumed.Draining || resumed.Status.Maintenance.Draining {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
}

func TestBackendDrainRefusesActiveCallWithoutEndingIt(t *testing.T) {
	call := newFakeVoiceCall()
	session := newFakeMediaSession()
	backend, err := NewBackendWithMediaStore(
		"line-1", "native", "process-1", &fakeFactory{run: &fakeRuntime{call: call}}, NewMemoryOperationStore(),
		fakeMediaDirectory{session: session}, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.StartCall(t.Context(), vowifiipc.StartCallRequest{
		OperationID: "call-start", CallID: "call-1", Callee: "+100", MediaBufferMS: 500,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.BeginDrain(t.Context(), vowifiipc.MaintenanceRequest{LeaseID: "apply-lease-1"}); operationCode(err) != "active_call" || call.ends.Load() != 0 {
		t.Fatalf("drain active call ends=%d err=%v", call.ends.Load(), err)
	}
	if _, err := backend.EndCall(t.Context(), vowifiipc.EndCallRequest{
		OperationID: "call-end", CallID: "call-1", ReasonCode: "test_cleanup",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackendDrainRefusesInFlightMessage(t *testing.T) {
	runtime := &fakeRuntime{messageStarted: make(chan struct{}, 1), messageRelease: make(chan struct{})}
	backend, err := NewBackend("line-1", "native", "process-1", &fakeFactory{run: runtime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := backend.SendMessage(t.Context(), vowifiipc.SendMessageRequest{
			OperationID: "message-send", MessageID: "message-1", Recipient: "+100", Body: "test",
		})
		done <- err
	}()
	select {
	case <-runtime.messageStarted:
	case <-time.After(time.Second):
		t.Fatal("message did not enter runtime")
	}
	if _, err := backend.BeginDrain(t.Context(), vowifiipc.MaintenanceRequest{LeaseID: "apply-lease-1"}); operationCode(err) != "operation_in_progress" {
		t.Fatalf("drain during message err=%v", err)
	}
	close(runtime.messageRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := backend.BeginDrain(t.Context(), vowifiipc.MaintenanceRequest{LeaseID: "apply-lease-1"}); err != nil {
		t.Fatal(err)
	}
}

type fakeFactory struct {
	starts atomic.Int32
	run    *fakeRuntime
	err    error
}

func (factory *fakeFactory) Start(context.Context) (Runtime, error) {
	factory.starts.Add(1)
	if factory.err != nil {
		if factory.run == nil {
			return nil, factory.err
		}
		return factory.run, factory.err
	}
	if factory.run == nil {
		return nil, nil
	}
	return factory.run, factory.err
}

type fakeRuntime struct {
	closes         atomic.Int32
	callStarts     atomic.Int32
	messages       atomic.Int32
	call           *fakeVoiceCall
	callErr        error
	messageErr     error
	messageStarted chan struct{}
	messageRelease chan struct{}
	closeErr       error
}

func (runtime *fakeRuntime) SendMessage(ctx context.Context, _ vowifiipc.SendMessageRequest) error {
	runtime.messages.Add(1)
	if runtime.messageStarted != nil {
		runtime.messageStarted <- struct{}{}
	}
	if runtime.messageRelease != nil {
		select {
		case <-runtime.messageRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return runtime.messageErr
}

func (*fakeRuntime) Layers() Layers {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	return Layers{Tunnel: ready, IMS: ready, Voice: ready, Messaging: ready}
}
func (runtime *fakeRuntime) Close(context.Context) error {
	runtime.closes.Add(1)
	return runtime.closeErr
}
func (runtime *fakeRuntime) StartMediaCall(context.Context, vowifiipc.StartCallRequest) (VoiceCall, error) {
	runtime.callStarts.Add(1)
	if runtime.callErr != nil {
		return nil, runtime.callErr
	}
	if runtime.call == nil {
		runtime.call = newFakeVoiceCall()
	}
	return runtime.call, nil
}

func TestBackendLifecycleAndIdempotency(t *testing.T) {
	runtime := &fakeRuntime{}
	factory := &fakeFactory{run: runtime}
	backend, err := NewBackend("line-1", "native", "process-1", factory)
	if err != nil {
		t.Fatal(err)
	}
	started, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"})
	if err != nil || started.Status.Runtime.Condition != vowifiipc.RuntimeRunning || !started.Status.Voice.Available {
		t.Fatalf("start=%+v err=%v", started, err)
	}
	replayed, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"})
	if err != nil || replayed.Code != "started" || factory.starts.Load() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", replayed, factory.starts.Load(), err)
	}
	_, err = backend.Stop(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"})
	var operationErr *vowifiipc.OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != "operation_id_reused" {
		t.Fatalf("reuse err=%v", err)
	}
	stopped, err := backend.Stop(context.Background(), vowifiipc.LifecycleRequest{OperationID: "stop-1"})
	if err != nil || stopped.Status.Runtime.Condition != vowifiipc.RuntimeStopped || runtime.closes.Load() != 1 {
		t.Fatalf("stop=%+v closes=%d err=%v", stopped, runtime.closes.Load(), err)
	}
}

func TestBackendFailedStartIsRetryableByNewOperation(t *testing.T) {
	factory := &fakeFactory{err: &StageError{Layer: "ims", Code: "ims_register_failed", Err: errors.New("rejected")}}
	backend, err := NewBackend("line-1", "native", "process-1", factory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"}); err == nil {
		t.Fatal("first start err=nil")
	} else {
		var operationErr *vowifiipc.OperationError
		if !errors.As(err, &operationErr) || operationErr.Layer != "ims" || operationErr.Code != "ims_register_failed" || operationErr.Detail != "rejected" {
			t.Fatalf("first start err=%#v", err)
		}
	}
	status, err := backend.Status(context.Background())
	if err != nil || status.Runtime.Condition != vowifiipc.RuntimeFailed || status.Runtime.Code != "ims_register_failed" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	factory.err = nil
	factory.run = &fakeRuntime{}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-2"}); err != nil {
		t.Fatalf("retry start: %v", err)
	}
}

func TestBackendReportsCloseFailureOnlyWhenFailedStartCleanupFails(t *testing.T) {
	runtime := &fakeRuntime{closeErr: errors.New("cleanup stuck")}
	backend, err := NewBackend("line-1", "native", "process-1", &fakeFactory{
		run: runtime,
		err: &StageError{Layer: "tunnel", Code: "swu_open_failed", Err: errors.New("IKE timeout")},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start-1"})
	var operationErr *vowifiipc.OperationError
	if !errors.As(err, &operationErr) || operationErr.Layer != "runtime" || operationErr.Code != "close_failed" || runtime.closes.Load() != 1 {
		t.Fatalf("start err=%#v closes=%d", err, runtime.closes.Load())
	}
}

func TestBackendDoesNotExposePaidActionsBeforeTransportExists(t *testing.T) {
	backend, err := NewBackend("line-1", "native", "process-1", &fakeFactory{run: &fakeRuntime{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"}); err != nil {
		t.Fatal(err)
	}
	_, err = backend.StartCall(context.Background(), vowifiipc.StartCallRequest{
		OperationID: "call-start-1", CallID: "call-1", Callee: "+1000000", MediaBufferMS: 500,
	})
	var operationErr *vowifiipc.OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != "browser_media_transport_unavailable" {
		t.Fatalf("start call err=%v", err)
	}
}

func TestBackendSendsMessageOnceAndPersistsExactIdempotencyFingerprint(t *testing.T) {
	runtime := &fakeRuntime{}
	backend, err := NewBackend("line-1", "native", "process-1", &fakeFactory{run: runtime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"}); err != nil {
		t.Fatal(err)
	}
	request := vowifiipc.SendMessageRequest{
		OperationID: "send-1", MessageID: "message-1", Recipient: "+1000000", Body: "hello",
	}
	result, err := backend.SendMessage(context.Background(), request)
	if err != nil || result.Code != "sent" || result.MessageID != request.MessageID || runtime.messages.Load() != 1 {
		t.Fatalf("result=%+v sends=%d err=%v", result, runtime.messages.Load(), err)
	}
	replayed, err := backend.SendMessage(context.Background(), request)
	if err != nil || replayed.Code != "sent" || runtime.messages.Load() != 1 {
		t.Fatalf("replay=%+v sends=%d err=%v", replayed, runtime.messages.Load(), err)
	}
	request.Body = "different"
	if _, err := backend.SendMessage(context.Background(), request); operationCode(err) != "operation_id_reused" {
		t.Fatalf("reused operation err=%v", err)
	}
}

func TestBackendPersistsMessageFailureWithoutBlindReplay(t *testing.T) {
	runtime := &fakeRuntime{messageErr: errors.New("SIP transport failed")}
	backend, _ := NewBackend("line-1", "native", "process-1", &fakeFactory{run: runtime})
	_, _ = backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "start-1"})
	request := vowifiipc.SendMessageRequest{
		OperationID: "send-1", MessageID: "message-1", Recipient: "+1000000", Body: "hello",
	}
	if _, err := backend.SendMessage(context.Background(), request); operationCode(err) != "message_send_failed" {
		t.Fatalf("first send err=%v", err)
	}
	if _, err := backend.SendMessage(context.Background(), request); operationCode(err) != "message_send_failed" {
		t.Fatalf("replayed failure err=%v", err)
	}
	if runtime.messages.Load() != 1 {
		t.Fatalf("failed message was sent %d times", runtime.messages.Load())
	}
}

func TestBackendBindsDurableCallToReadyMediaAndEndsIt(t *testing.T) {
	call := newFakeVoiceCall()
	runtime := &fakeRuntime{call: call}
	session := newFakeMediaSession()
	backend, err := NewBackendWithMediaStore(
		"line-1", "native", "process-1", &fakeFactory{run: runtime}, NewMemoryOperationStore(),
		fakeMediaDirectory{session: session}, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	request := vowifiipc.StartCallRequest{
		OperationID: "call-start", CallID: "call-1", Callee: "+1000000", MediaBufferMS: 500,
	}
	started, err := backend.StartCall(context.Background(), request)
	if err != nil || started.Code != "active" || started.Status.ActiveCall == nil ||
		started.Status.ActiveCall.Condition != vowifiipc.CallActive {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	if replay, err := backend.StartCall(context.Background(), request); err != nil || replay.Code != "active" || runtime.callStarts.Load() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", replay, runtime.callStarts.Load(), err)
	}
	reused := request
	reused.Callee = "+2000000"
	if _, err := backend.StartCall(context.Background(), reused); operationCode(err) != "operation_id_reused" {
		t.Fatalf("reused operation err=%v", err)
	}
	ended, err := backend.EndCall(context.Background(), vowifiipc.EndCallRequest{
		OperationID: "call-end", CallID: request.CallID, ReasonCode: "user_hangup",
	})
	if err != nil || ended.Code != "ended" || ended.Status.ActiveCall != nil || call.ends.Load() != 1 || !session.ended.Load() {
		t.Fatalf("ended=%+v ends=%d sessionEnded=%v err=%v", ended, call.ends.Load(), session.ended.Load(), err)
	}
}

func TestBackendCallGuardEndsOnlyExactCallAfterHeartbeatTimeout(t *testing.T) {
	call := newFakeVoiceCall()
	runtime := &fakeRuntime{call: call}
	session := newFakeMediaSession()
	backend, err := NewBackendWithMediaStore(
		"line-1", "native", "process-1", &fakeFactory{run: runtime}, NewMemoryOperationStore(),
		fakeMediaDirectory{session: session}, 20*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.StartCall(context.Background(), vowifiipc.StartCallRequest{
		OperationID: "call-start", CallID: "call-1", Callee: "+1000000", MediaBufferMS: 500,
	}); err != nil {
		t.Fatal(err)
	}
	session.setConnected(false, time.Now().Add(-time.Second))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && call.ends.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if call.ends.Load() != 1 || !session.ended.Load() {
		t.Fatalf("guard ends=%d sessionEnded=%v", call.ends.Load(), session.ended.Load())
	}
}

func TestBackendCallGuardKeepsCallAcrossBoundedReconnect(t *testing.T) {
	call := newFakeVoiceCall()
	runtime := &fakeRuntime{call: call}
	session := newFakeMediaSession()
	backend, err := NewBackendWithMediaStore(
		"line-1", "native", "process-1", &fakeFactory{run: runtime}, NewMemoryOperationStore(),
		fakeMediaDirectory{session: session}, 40*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.StartCall(context.Background(), vowifiipc.StartCallRequest{
		OperationID: "call-start", CallID: "call-1", Callee: "+1000000", MediaBufferMS: 500,
	}); err != nil {
		t.Fatal(err)
	}
	session.setConnected(false, time.Now())
	time.Sleep(10 * time.Millisecond)
	for index := 0; index < 6; index++ {
		session.setConnected(true, time.Now())
		time.Sleep(10 * time.Millisecond)
	}
	if call.ends.Load() != 0 {
		t.Fatalf("bounded reconnect ended call %d times", call.ends.Load())
	}
	if _, err := backend.EndCall(context.Background(), vowifiipc.EndCallRequest{
		OperationID: "call-end", CallID: "call-1", ReasonCode: "test_cleanup",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackendStopEndsPaidCallBeforeClosingRuntime(t *testing.T) {
	call := newFakeVoiceCall()
	runtime := &fakeRuntime{call: call}
	session := newFakeMediaSession()
	backend, err := NewBackendWithMediaStore(
		"line-1", "native", "process-1", &fakeFactory{run: runtime}, NewMemoryOperationStore(),
		fakeMediaDirectory{session: session}, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(context.Background(), vowifiipc.LifecycleRequest{OperationID: "runtime-start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.StartCall(context.Background(), vowifiipc.StartCallRequest{
		OperationID: "call-start", CallID: "call-1", Callee: "+1000000", MediaBufferMS: 500,
	}); err != nil {
		t.Fatal(err)
	}
	stopped, err := backend.Stop(context.Background(), vowifiipc.LifecycleRequest{OperationID: "runtime-stop"})
	if err != nil || stopped.Status.Runtime.Condition != vowifiipc.RuntimeStopped ||
		call.ends.Load() != 1 || runtime.closes.Load() != 1 || !session.ended.Load() {
		t.Fatalf("stopped=%+v ends=%d closes=%d sessionEnded=%v err=%v",
			stopped, call.ends.Load(), runtime.closes.Load(), session.ended.Load(), err)
	}
}

type fakeVoiceCall struct {
	ends   atomic.Int32
	input  chan []byte
	output chan media.PCMFrame
	errors chan error
}

func newFakeVoiceCall() *fakeVoiceCall {
	return &fakeVoiceCall{input: make(chan []byte, 1), output: make(chan media.PCMFrame, 1), errors: make(chan error)}
}

func (call *fakeVoiceCall) End(context.Context) (voicehost.DialogInfoResult, error) {
	call.ends.Add(1)
	return voicehost.DialogInfoResult{Accepted: true, StatusCode: 200}, nil
}
func (call *fakeVoiceCall) WritePCM(frame []byte, _ time.Time) (bool, error) {
	call.input <- append([]byte(nil), frame...)
	return true, nil
}
func (call *fakeVoiceCall) PCM() <-chan media.PCMFrame { return call.output }
func (call *fakeVoiceCall) Errors() <-chan error       { return call.errors }

type fakeMediaDirectory struct{ session *fakeMediaSession }

func (directory fakeMediaDirectory) Lookup(id string) (BrowserMediaSession, bool) {
	return directory.session, id == "call-1"
}

type fakeMediaSession struct {
	mu        sync.Mutex
	ready     bool
	connected bool
	lastSeen  time.Time
	changes   chan struct{}
	attached  browsermedia.Stream
	ended     atomic.Bool
}

func newFakeMediaSession() *fakeMediaSession {
	return &fakeMediaSession{ready: true, connected: true, lastSeen: time.Now(), changes: make(chan struct{}, 1)}
}
func (session *fakeMediaSession) Ready() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.ready
}
func (session *fakeMediaSession) Connected() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.connected
}
func (session *fakeMediaSession) LastSeen() time.Time {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.lastSeen
}
func (session *fakeMediaSession) Changes() <-chan struct{} { return session.changes }
func (session *fakeMediaSession) AttachStream(stream browsermedia.Stream) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.ready || !session.connected || session.attached != nil {
		return errors.New("not attachable")
	}
	session.attached = stream
	return nil
}
func (session *fakeMediaSession) EndStream(string) { session.ended.Store(true) }
func (session *fakeMediaSession) setConnected(connected bool, seen time.Time) {
	session.mu.Lock()
	session.connected, session.lastSeen = connected, seen
	session.mu.Unlock()
	select {
	case session.changes <- struct{}{}:
	default:
	}
}

func operationCode(err error) string {
	var failure *vowifiipc.OperationError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func TestStatusHeartbeatAdvancesSequence(t *testing.T) {
	backend, err := NewBackend("line-1", "native", "process-1", &fakeFactory{run: &fakeRuntime{}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := backend.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := backend.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Sequence != first.Sequence+1 || second.ObservedAt.Before(first.ObservedAt) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}
