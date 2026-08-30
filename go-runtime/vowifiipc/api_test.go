package vowifiipc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "test-vowifi-ipc-token-32-bytes-long"

type fakeBackend struct {
	mu         sync.Mutex
	snapshot   Snapshot
	drainLease string
	lastStop   LifecycleRequest
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{snapshot: Snapshot{
		SchemaVersion: SchemaVersion, LineID: "line-1", ProviderID: "provider-1",
		ProcessGeneration: "process-1", Sequence: 1, ObservedAt: time.Now().UTC(),
		Runtime: RuntimeStatus{Condition: RuntimeStopped},
		Tunnel:  LayerStatus{Condition: LayerStopped}, IMS: LayerStatus{Condition: LayerStopped},
		Voice: LayerStatus{Condition: LayerStopped}, Messaging: LayerStatus{Condition: LayerStopped},
	}}
}

func (backend *fakeBackend) Status(context.Context) (Snapshot, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return cloneSnapshot(backend.snapshot), nil
}

func (backend *fakeBackend) Start(_ context.Context, input LifecycleRequest) (OperationResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.snapshot.Runtime.Condition != RuntimeStopped {
		return OperationResult{}, &OperationError{Kind: ErrorConflict, Code: "runtime_conflict", Layer: "runtime"}
	}
	backend.advance()
	backend.snapshot.Runtime = RuntimeStatus{Condition: RuntimeRunning}
	ready := LayerStatus{Condition: LayerReady, Available: true}
	backend.snapshot.Tunnel, backend.snapshot.IMS = ready, ready
	backend.snapshot.Voice, backend.snapshot.Messaging = ready, ready
	return OperationResult{OperationID: input.OperationID, Accepted: true, Status: cloneSnapshot(backend.snapshot)}, nil
}

func (backend *fakeBackend) Stop(_ context.Context, input LifecycleRequest) (OperationResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.lastStop = input
	if backend.snapshot.ActiveCall != nil {
		return OperationResult{}, &OperationError{Kind: ErrorConflict, Code: "call_active", Layer: "call"}
	}
	backend.advance()
	backend.snapshot.Runtime = RuntimeStatus{Condition: RuntimeStopped}
	stopped := LayerStatus{Condition: LayerStopped}
	backend.snapshot.Tunnel, backend.snapshot.IMS = stopped, stopped
	backend.snapshot.Voice, backend.snapshot.Messaging = stopped, stopped
	return OperationResult{OperationID: input.OperationID, Accepted: true, Status: cloneSnapshot(backend.snapshot)}, nil
}

func TestLifecycleRequireIdleRoundTripsToProvider(t *testing.T) {
	backend := newFakeBackend()
	api, err := NewAPI(backend, testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	defer server.Close()
	client, err := NewClient(server.URL, testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	if supported, err := client.SupportsRecoveryStop(t.Context()); err != nil || !supported {
		t.Fatalf("recovery stop capability supported=%v err=%v", supported, err)
	}
	if _, err := client.Stop(t.Context(), LifecycleRequest{OperationID: "recovery-stop", RequireIdle: true}); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	request := backend.lastStop
	backend.mu.Unlock()
	if request.OperationID != "recovery-stop" || !request.RequireIdle {
		t.Fatalf("stop request=%+v", request)
	}
}

func (backend *fakeBackend) StartCall(_ context.Context, input StartCallRequest) (CallResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.snapshot.Runtime.Condition != RuntimeRunning || !backend.snapshot.Voice.Available {
		return CallResult{}, &OperationError{Kind: ErrorNotReady, Code: "voice_not_ready", Layer: "voice"}
	}
	if backend.snapshot.ActiveCall != nil {
		return CallResult{}, &OperationError{Kind: ErrorConflict, Code: "line_busy", Layer: "call"}
	}
	backend.advance()
	backend.snapshot.ActiveCall = &ActiveCall{CallID: input.CallID, Condition: CallActive}
	return CallResult{OperationResult: OperationResult{
		OperationID: input.OperationID, Accepted: true, Status: cloneSnapshot(backend.snapshot),
	}, CallID: input.CallID}, nil
}

func (backend *fakeBackend) EndCall(_ context.Context, input EndCallRequest) (CallResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.snapshot.ActiveCall == nil || backend.snapshot.ActiveCall.CallID != input.CallID {
		return CallResult{}, &OperationError{Kind: ErrorNotFound, Code: "call_not_found", Layer: "call"}
	}
	backend.advance()
	backend.snapshot.ActiveCall = nil
	return CallResult{OperationResult: OperationResult{
		OperationID: input.OperationID, Accepted: true, Status: cloneSnapshot(backend.snapshot),
	}, CallID: input.CallID}, nil
}

func (backend *fakeBackend) SendDTMF(_ context.Context, input SendDTMFRequest) (CallResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.snapshot.ActiveCall == nil || backend.snapshot.ActiveCall.CallID != input.CallID {
		return CallResult{}, &OperationError{Kind: ErrorConflict, Code: "call_not_active", Layer: "call"}
	}
	backend.advance()
	return CallResult{OperationResult: OperationResult{
		OperationID: input.OperationID, Accepted: true, Code: "dtmf_rtp", Status: cloneSnapshot(backend.snapshot),
	}, CallID: input.CallID}, nil
}

func (backend *fakeBackend) AnswerIncomingCall(_ context.Context, input AnswerIncomingCallRequest) (CallResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.snapshot.ActiveCall != nil {
		return CallResult{}, &OperationError{Kind: ErrorConflict, Code: "line_busy", Layer: "call"}
	}
	backend.advance()
	backend.snapshot.PendingIncomingCall = nil
	backend.snapshot.ActiveCall = &ActiveCall{CallID: input.CallID, Condition: CallActive}
	return CallResult{OperationResult: OperationResult{
		OperationID: input.OperationID, Accepted: true, Code: "active", Status: cloneSnapshot(backend.snapshot),
	}, CallID: input.CallID}, nil
}

func (backend *fakeBackend) RejectIncomingCall(_ context.Context, input RejectIncomingCallRequest) (CallResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.advance()
	backend.snapshot.PendingIncomingCall = nil
	return CallResult{OperationResult: OperationResult{
		OperationID: input.OperationID, Accepted: true, Code: "rejected", Status: cloneSnapshot(backend.snapshot),
	}, CallID: input.CallID}, nil
}

func (backend *fakeBackend) SendMessage(_ context.Context, input SendMessageRequest) (MessageResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.snapshot.Runtime.Condition != RuntimeRunning || !backend.snapshot.Messaging.Available {
		return MessageResult{}, &OperationError{Kind: ErrorNotReady, Code: "messaging_not_ready", Layer: "messaging"}
	}
	backend.advance()
	return MessageResult{OperationResult: OperationResult{
		OperationID: input.OperationID, Accepted: true, Status: cloneSnapshot(backend.snapshot),
	}, MessageID: input.MessageID}, nil
}

func (backend *fakeBackend) BeginDrain(_ context.Context, input MaintenanceRequest) (MaintenanceResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.snapshot.ActiveCall != nil {
		return MaintenanceResult{}, &OperationError{Kind: ErrorConflict, Code: "active_call", Layer: "maintenance"}
	}
	if backend.drainLease != "" && backend.drainLease != input.LeaseID {
		return MaintenanceResult{}, &OperationError{Kind: ErrorConflict, Code: "maintenance_busy", Layer: "maintenance"}
	}
	backend.drainLease = input.LeaseID
	backend.snapshot.Maintenance = MaintenanceStatus{Draining: true, Code: "apply_drain", LeaseID: input.LeaseID}
	backend.advance()
	return MaintenanceResult{LeaseID: input.LeaseID, Draining: true, Status: cloneSnapshot(backend.snapshot)}, nil
}

func (backend *fakeBackend) EndDrain(_ context.Context, input MaintenanceRequest) (MaintenanceResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.drainLease != input.LeaseID {
		return MaintenanceResult{}, &OperationError{Kind: ErrorConflict, Code: "maintenance_lease_mismatch", Layer: "maintenance"}
	}
	backend.drainLease = ""
	backend.snapshot.Maintenance = MaintenanceStatus{}
	backend.advance()
	return MaintenanceResult{LeaseID: input.LeaseID, Draining: false, Status: cloneSnapshot(backend.snapshot)}, nil
}

func (backend *fakeBackend) advance() {
	backend.snapshot.Sequence++
	backend.snapshot.ObservedAt = time.Now().UTC()
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.ActiveCall != nil {
		call := *snapshot.ActiveCall
		snapshot.ActiveCall = &call
	}
	if snapshot.PendingIncomingCall != nil {
		pending := *snapshot.PendingIncomingCall
		snapshot.PendingIncomingCall = &pending
	}
	return snapshot
}

func TestClientAndAPIRejectInvalidAuthorityAndInput(t *testing.T) {
	api, err := NewAPI(newFakeBackend(), testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	defer server.Close()

	wrong, err := NewClient(server.URL, strings.Repeat("x", 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrong.Status(context.Background())
	var responseError *ResponseError
	if !errors.As(err, &responseError) || responseError.Status != http.StatusUnauthorized ||
		responseError.Failure.Code != "unauthorized" {
		t.Fatalf("wrong-token Status() error = %v", err)
	}
	if _, err := NewClient("http://example.com:8080", testToken, nil); err == nil {
		t.Fatal("non-loopback endpoint was accepted")
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/runtime/start",
		strings.NewReader(`{"operation_id":"op-1","unexpected":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown-field status = %d", response.StatusCode)
	}

	tooLarge := append([]byte(`{"operation_id":"message-send-large","message_id":"message-large","recipient":"+100","body":"`),
		bytes.Repeat([]byte("x"), maxRequestBytes+1)...)
	tooLarge = append(tooLarge, []byte(`"}`)...)
	request, err = http.NewRequest(http.MethodPost, server.URL+"/v1/messages/send", bytes.NewReader(tooLarge))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d", response.StatusCode)
	}
}

func TestAPIRejectsRequestThatDidNotArriveFromLoopback(t *testing.T) {
	api, err := NewAPI(newFakeBackend(), testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/status", nil)
	request.RemoteAddr = "203.0.113.7:12345"
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"local_only"`) {
		t.Fatalf("remote request = HTTP %d %s", response.Code, response.Body.String())
	}
}

func TestClientRejectsProviderSnapshotThatCannotBeAuthoritative(t *testing.T) {
	backend := newFakeBackend()
	backend.snapshot.Sequence = 0
	api, err := NewAPI(backend, testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	defer server.Close()
	client, err := NewClient(server.URL, testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Status(context.Background())
	var responseError *ResponseError
	if !errors.As(err, &responseError) || responseError.Status != http.StatusInternalServerError ||
		responseError.Failure.Code != "operation_failed" {
		t.Fatalf("invalid snapshot error = %v", err)
	}
}

func TestIPCWholeControlFlowAcrossRealProcess(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestVowifiIPCSubprocess$")
	command.Env = append(os.Environ(), "MDD_VOWIFI_IPC_HELPER=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := false
	defer func() {
		if exited {
			return
		}
		_ = command.Process.Kill()
		_ = command.Wait()
	}()

	ready := make(chan string, 1)
	readError := make(chan error, 1)
	go func() {
		wire, err := bufio.NewReader(io.LimitReader(stdout, 1024)).ReadString('\n')
		if err != nil {
			readError <- err
			return
		}
		ready <- strings.TrimSpace(string(wire))
	}()
	var baseURL string
	select {
	case baseURL = <-ready:
	case err := <-readError:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatalf("helper did not become ready: %s", stderr.String())
	}
	client, err := NewClient(baseURL, testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if status, err := client.Status(ctx); err != nil || status.Runtime.Condition != RuntimeStopped {
		t.Fatalf("initial Status() = %+v, %v", status, err)
	}
	if result, err := client.Start(ctx, LifecycleRequest{OperationID: "start-1"}); err != nil ||
		result.Status.Runtime.Condition != RuntimeRunning || !result.Status.Voice.Available {
		t.Fatalf("Start() = %+v, %v", result, err)
	}
	call, err := client.StartCall(ctx, StartCallRequest{
		OperationID: "call-start-1", CallID: "call-1", Callee: "+100", MediaBufferMS: 500,
	})
	if err != nil || call.Status.ActiveCall == nil || call.Status.ActiveCall.CallID != "call-1" {
		t.Fatalf("StartCall() = %+v, %v", call, err)
	}
	if _, err := client.StartCall(ctx, StartCallRequest{
		OperationID: "call-start-2", CallID: "call-2", Callee: "+101", MediaBufferMS: 500,
	}); !responseCode(err, http.StatusConflict, "line_busy") {
		t.Fatalf("second StartCall() error = %v", err)
	}
	dtmf, err := client.SendDTMF(ctx, SendDTMFRequest{
		OperationID: "call-dtmf-1", CallID: "call-1", Signal: "5", DurationMS: 160,
	})
	if err != nil || dtmf.Code != "dtmf_rtp" || dtmf.CallID != "call-1" {
		t.Fatalf("SendDTMF() = %+v, %v", dtmf, err)
	}
	ended, err := client.EndCall(ctx, EndCallRequest{
		OperationID: "call-end-1", CallID: "call-1", ReasonCode: "browser_hangup",
	})
	if err != nil || ended.Status.ActiveCall != nil {
		t.Fatalf("EndCall() = %+v, %v", ended, err)
	}
	answered, err := client.AnswerIncomingCall(ctx, AnswerIncomingCallRequest{
		OperationID: "incoming-answer-1", CallID: "incoming-1", MediaBufferMS: 500,
	})
	if err != nil || answered.Status.ActiveCall == nil || answered.Status.ActiveCall.CallID != "incoming-1" {
		t.Fatalf("AnswerIncomingCall() = %+v, %v", answered, err)
	}
	if _, err := client.EndCall(ctx, EndCallRequest{
		OperationID: "incoming-end-1", CallID: "incoming-1", ReasonCode: "browser_hangup",
	}); err != nil {
		t.Fatalf("EndCall(incoming) = %v", err)
	}
	rejected, err := client.RejectIncomingCall(ctx, RejectIncomingCallRequest{
		OperationID: "incoming-reject-1", CallID: "incoming-2", ReasonCode: "user_rejected",
	})
	if err != nil || rejected.Code != "rejected" {
		t.Fatalf("RejectIncomingCall() = %+v, %v", rejected, err)
	}
	message, err := client.SendMessage(ctx, SendMessageRequest{
		OperationID: "message-send-1", MessageID: "message-1", Recipient: "+100", Body: "test",
	})
	if err != nil || message.MessageID != "message-1" {
		t.Fatalf("SendMessage() = %+v, %v", message, err)
	}
	drained, err := client.BeginDrain(ctx, MaintenanceRequest{LeaseID: "apply-lease-1"})
	if err != nil || !drained.Draining || !drained.Status.Maintenance.Draining {
		t.Fatalf("BeginDrain() = %+v, %v", drained, err)
	}
	resumed, err := client.EndDrain(ctx, MaintenanceRequest{LeaseID: "apply-lease-1"})
	if err != nil || resumed.Draining || resumed.Status.Maintenance.Draining {
		t.Fatalf("EndDrain() = %+v, %v", resumed, err)
	}
	stopped, err := client.Stop(ctx, LifecycleRequest{OperationID: "stop-1"})
	if err != nil || stopped.Status.Runtime.Condition != RuntimeStopped {
		t.Fatalf("Stop() = %+v, %v", stopped, err)
	}

	if err := command.Process.Signal(os.Interrupt); err != nil {
		_ = command.Process.Kill()
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		exited = true
		if err != nil {
			t.Fatalf("helper exit: %v: %s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("helper did not stop: %s", stderr.String())
	}
}

func TestVowifiIPCSubprocess(t *testing.T) {
	if os.Getenv("MDD_VOWIFI_IPC_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(newFakeBackend(), testToken, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: api, ReadHeaderTimeout: 2 * time.Second}
	serverError := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverError <- err
			return
		}
		serverError <- nil
	}()
	if _, err := fmt.Fprintf(os.Stdout, "http://%s\n", listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	stopContext, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	<-stopContext.Done()
	stop()
	shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
}

func responseCode(err error, status int, code string) bool {
	var responseError *ResponseError
	return errors.As(err, &responseError) && responseError.Status == status && responseError.Failure.Code == code
}
