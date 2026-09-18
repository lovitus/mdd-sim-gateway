package service

import (
	"context"
	"errors"
	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"testing"
	"time"
)

type healthReviewRuntime struct {
	*fakeRuntime
	health vowifiipc.RuntimeHealth
}

func (r *healthReviewRuntime) StatusWithHealth() (Layers, *vowifiipc.RuntimeHealth) {
	copy := r.health
	return r.Layers(), &copy
}

func TestLivenessReviewProviderRechecksCarrierDeadline(t *testing.T) {
	future := time.Now().Add(time.Hour)
	runtime := &healthReviewRuntime{fakeRuntime: &fakeRuntime{}, health: vowifiipc.RuntimeHealth{IMSFailures: 4, IMSRetryAfterUntil: &future}}
	backend, err := NewBackend("line-1", "native", "generation-1", reviewFactory{runtime})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"})
	if err != nil {
		t.Fatal(err)
	}
	// Even a stale Core recovery decision cannot bypass the live carrier deadline.
	_, err = backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "recover", RequireIdle: true})
	if operationCode(err) != "ims_retry_wait" || runtime.closes.Load() != 0 {
		t.Fatalf("carrier holdoff bypassed: %v", err)
	}
	// An explicit user stop remains possible, but an immediate new start does not
	// bypass the same Provider's remembered carrier restriction.
	_, err = backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "user-stop"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "too-soon"})
	if operationCode(err) != "ims_retry_wait" {
		t.Fatalf("stop/start bypassed Retry-After: %v", err)
	}
}

func TestLivenessReviewProviderReturnsProgressNotSuccessClaim(t *testing.T) {
	for _, progress := range []error{runtimehost.ErrIMSRegistrationInProgress, runtimehost.ErrIMSRegistrationRetryPending} {
		runtime := &fakeRuntime{registerErr: progress}
		backend, _ := NewBackend("line-1", "native", "generation-1", &fakeFactory{run: runtime})
		if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil {
			t.Fatal(err)
		}
		result, err := backend.Register(t.Context(), vowifiipc.RegisterRequest{OperationID: "register"})
		if err != nil || result.Code != registrationProgressCode(progress) || result.Code == "ims_registered" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestLivenessReviewManualRuntimeRecoveryCannotQueue(t *testing.T) {
	runtime := &upstreamRuntime{}
	runtime.recoveryMu.Lock()
	defer runtime.recoveryMu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, _, err := runtime.recoverCallRegistration(ctx, 0, 0)
	if !errors.Is(err, runtimehost.ErrIMSRegistrationInProgress) {
		t.Fatalf("busy=%v", err)
	}
	cancel()
	_, _, err = runtime.recoverCallRegistration(ctx, 0, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled=%v", err)
	}
}

func TestLivenessReviewPeerInboundDoesNotRefreshOnReplay(t *testing.T) {
	peer, init := peerFixture(t)
	calls := 0
	peer.onAuthenticated = func(time.Time) { calls++ }
	request := peerRequest(t, init, 1)
	if _, err := peer.handle(request); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.handle(request); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), peerRequest(t, init, 2)...)
	bad[len(bad)-1] ^= 1
	if _, err := peer.handle(bad); err == nil {
		t.Fatal("forged request authenticated")
	}
	if calls != 1 {
		t.Fatalf("fresh authenticated observations=%d", calls)
	}
}

type initialRetryFactory struct{ calls int }

func (f *initialRetryFactory) Start(context.Context) (Runtime, error) {
	f.calls++
	return nil, &StageError{Layer: "ims", Code: "ims_register_failed", RetryAfter: time.Hour, Err: errors.New("retry later")}
}
func TestLivenessReviewInitialFailureCannotBypassRetryAfter(t *testing.T) {
	factory := &initialRetryFactory{}
	backend, _ := NewBackend("line-1", "native", "generation", factory)
	_, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"})
	var failure *vowifiipc.OperationError
	if !errors.As(err, &failure) || failure.RetryAfter < 59*time.Minute {
		t.Fatalf("Retry-After not propagated: %v", err)
	}
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "stop"}); err != nil {
		t.Fatal(err)
	}
	_, err = backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "repeat"})
	if operationCode(err) != "ims_retry_wait" || factory.calls != 1 {
		t.Fatalf("outer restart bypassed initial Retry-After: %v calls=%d", err, factory.calls)
	}
}
