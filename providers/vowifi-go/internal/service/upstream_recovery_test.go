// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
)

func TestUpstreamRuntimeRecoversTransportFailureAndRetriesOnce(t *testing.T) {
	transportErr := errors.New("stale P-CSCF flow")
	recoveries := 0
	recoveredRegistration := runtimehost.IMSRegistrationResult{Registered: true, Reason: "recovered"}
	runtime := &upstreamRuntime{registration: runtimehost.IMSRegistrationResult{
		Registered: true,
		Reason:     "initial",
		Recover: func(context.Context) (runtimehost.IMSRegistrationResult, error) {
			recoveries++
			return recoveredRegistration, nil
		},
	}}
	wantCall := newFakeVoiceCall()
	attempts := 0
	call, result, err := runtime.startMediaCallWithRecovery(t.Context(), func(registration runtimehost.IMSRegistrationResult) (VoiceCall, voicehost.OutboundCallResult, error) {
		attempts++
		if attempts == 1 {
			if registration.Reason != "initial" {
				t.Fatalf("first registration reason=%q", registration.Reason)
			}
			return nil, voicehost.OutboundCallResult{
				Accepted: false, Reason: "IMS INVITE failed", RegistrationRecoveryNeeded: true,
			}, transportErr
		}
		if registration.Reason != "recovered" {
			t.Fatalf("retry registration reason=%q", registration.Reason)
		}
		return wantCall, voicehost.OutboundCallResult{Accepted: true}, nil
	})
	if err != nil || !result.Accepted || call != wantCall {
		t.Fatalf("call=%T result=%+v err=%v", call, result, err)
	}
	if attempts != 2 || recoveries != 1 {
		t.Fatalf("attempts=%d recoveries=%d, want 2/1", attempts, recoveries)
	}
	registration, revision := runtime.registrationSnapshot()
	if registration.Reason != "recovered" || revision != 1 {
		t.Fatalf("registration=%+v revision=%d", registration, revision)
	}
}

func TestUpstreamRuntimeDoesNotRedialCarrierResponse(t *testing.T) {
	recoveries := 0
	runtime := &upstreamRuntime{registration: runtimehost.IMSRegistrationResult{
		Registered: true,
		Recover: func(context.Context) (runtimehost.IMSRegistrationResult, error) {
			recoveries++
			return runtimehost.IMSRegistrationResult{Registered: true}, nil
		},
	}}
	attempts := 0
	call, result, err := runtime.startMediaCallWithRecovery(t.Context(), func(runtimehost.IMSRegistrationResult) (VoiceCall, voicehost.OutboundCallResult, error) {
		attempts++
		return nil, voicehost.OutboundCallResult{
			Accepted: false, StatusCode: 503, Reason: "Service Unavailable", RegistrationRecoveryNeeded: true,
		}, nil
	})
	if err != nil || call != nil || result.StatusCode != 503 {
		t.Fatalf("call=%T result=%+v err=%v", call, result, err)
	}
	if attempts != 1 || recoveries != 1 {
		t.Fatalf("attempts=%d recoveries=%d, want 1/1", attempts, recoveries)
	}
}

func TestUpstreamRuntimeBoundsRecoveryRetryToOne(t *testing.T) {
	firstErr := errors.New("first transport failure")
	secondErr := errors.New("second transport failure")
	runtime := &upstreamRuntime{registration: runtimehost.IMSRegistrationResult{
		Registered: true,
		Recover: func(context.Context) (runtimehost.IMSRegistrationResult, error) {
			return runtimehost.IMSRegistrationResult{Registered: true}, nil
		},
	}}
	attempts := 0
	_, _, err := runtime.startMediaCallWithRecovery(t.Context(), func(runtimehost.IMSRegistrationResult) (VoiceCall, voicehost.OutboundCallResult, error) {
		attempts++
		if attempts == 1 {
			return nil, voicehost.OutboundCallResult{RegistrationRecoveryNeeded: true}, firstErr
		}
		return nil, voicehost.OutboundCallResult{RegistrationRecoveryNeeded: true}, secondErr
	})
	if !errors.Is(err, secondErr) || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestUpstreamRuntimePreservesCallAndRecoveryFailures(t *testing.T) {
	callErr := errors.New("INVITE timeout")
	recoveryErr := errors.New("REGISTER timeout")
	runtime := &upstreamRuntime{registration: runtimehost.IMSRegistrationResult{
		Registered: true,
		Recover: func(context.Context) (runtimehost.IMSRegistrationResult, error) {
			return runtimehost.IMSRegistrationResult{}, recoveryErr
		},
	}}
	_, _, err := runtime.startMediaCallWithRecovery(t.Context(), func(runtimehost.IMSRegistrationResult) (VoiceCall, voicehost.OutboundCallResult, error) {
		return nil, voicehost.OutboundCallResult{RegistrationRecoveryNeeded: true}, callErr
	})
	if !errors.Is(err, callErr) || !errors.Is(err, recoveryErr) {
		t.Fatalf("err=%v, want both failures", err)
	}
}
