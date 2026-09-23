package service

import (
	"context"
	"errors"
	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"testing"
)

func TestReviewFixOnlyProtocolProofCanBecomeRejectedOutcome(t *testing.T) {
	for _, result := range []voicehost.OutboundCallResult{{StatusCode: 486}, {StatusCode: 503}, {DialogEstablished: true, RejectionConfirmed: true, StatusCode: 486}, {Accepted: true, RejectionConfirmed: true}} {
		_, err := mediaCallOutcome(nil, result, errors.New("synthetic transport uncertainty"))
		var rejected *confirmedCallRejection
		if errors.As(err, &rejected) {
			t.Fatalf("unproved error minted rejection: %+v", result)
		}
	}
	var rejected *confirmedCallRejection
	_, err := mediaCallOutcome(nil, voicehost.OutboundCallResult{RejectionConfirmed: true, StatusCode: 486}, nil)
	if !errors.As(err, &rejected) {
		t.Fatal("lost protocol proof")
	}
	var failure *vowifiipc.OperationError
	if !errors.As(err, &failure) || failure.Code != "call_rejected" {
		t.Fatal("lost public result")
	}
}

// A definitive final response remains authoritative even when ACK transport
// also reports an error. Registration refresh must not repeat the paid start.
func TestReviewFixRejectionWithTransportErrorNeverRedials(t *testing.T) {
	ackErr := errors.New("synthetic rejection ACK failure")
	recoveries, attempts := 0, 0
	runtime := &upstreamRuntime{registration: runtimehost.IMSRegistrationResult{
		Registered: true,
		Recover: func(context.Context) (runtimehost.IMSRegistrationResult, error) {
			recoveries++
			return runtimehost.IMSRegistrationResult{Registered: true}, nil
		},
	}}
	call, result, err := runtime.startMediaCallWithRecovery(t.Context(), func(runtimehost.IMSRegistrationResult) (VoiceCall, voicehost.OutboundCallResult, error) {
		attempts++
		return nil, voicehost.OutboundCallResult{StatusCode: 503, RejectionConfirmed: true, RegistrationRecoveryNeeded: true}, ackErr
	})
	if attempts != 1 || recoveries != 1 || call != nil || !result.RejectionConfirmed || !errors.Is(err, ackErr) {
		t.Fatalf("attempts=%d recoveries=%d call=%T result=%+v err=%v", attempts, recoveries, call, result, err)
	}
}
