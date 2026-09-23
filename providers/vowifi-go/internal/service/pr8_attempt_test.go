// SPDX-License-Identifier: AGPL-3.0-only
package service

import (
	"context"
	"errors"
	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"testing"
)

func TestPR8CleanupHandleSurvivesClassificationAndPreventsStartReplay(t *testing.T) {
	call := newFakeVoiceCall()
	for _, err := range []error{errors.New("post acceptance failure"), &StageError{Layer: "voice", Code: "test", Err: errors.New("nested")}} {
		got, cause := classifyMediaCallAttempt(call, voicehost.OutboundCallResult{RegistrationRecoveryNeeded: true}, err)
		if got != call || cause == nil {
			t.Fatal("classification discarded original cleanup")
		}
	}
	attempts := 0
	run := &upstreamRuntime{}
	got, _, err := run.startMediaCallWithRecovery(t.Context(), func(runtimehost.IMSRegistrationResult) (VoiceCall, voicehost.OutboundCallResult, error) {
		attempts++
		return call, voicehost.OutboundCallResult{RegistrationRecoveryNeeded: true}, errors.New("accepted media failure")
	})
	if got != call || err == nil || attempts != 1 {
		t.Fatal("cleanup owner caused a second start", attempts, err)
	}
}
func TestPR8RejectionEvidenceDoesNotIncludeTransportOrLocalErrors(t *testing.T) {
	for _, result := range []voicehost.OutboundCallResult{
		{}, {StatusCode: 486}, {FinalRejected: true, StatusCode: 200}, {FinalRejected: true, StatusCode: 486, Accepted: true},
	} {
		_, err := classifyMediaCallAttempt(nil, result, context.DeadlineExceeded)
		if errors.Is(err, errConfirmedCallRejection) {
			t.Fatal("uncertain start classified terminal", result)
		}
	}
	_, err := classifyMediaCallAttempt(nil, voicehost.OutboundCallResult{FinalRejected: true, StatusCode: 486}, nil)
	if !errors.Is(err, errConfirmedCallRejection) {
		t.Fatal("real final response missing rejection evidence")
	}
}

func TestPR8RejectionRemainsDefinitiveWhenRegistrationRefreshFails(t *testing.T) {
	refreshErr := errors.New("registration temporarily unavailable")
	run := &upstreamRuntime{registration: runtimehost.IMSRegistrationResult{Recover: func(context.Context) (runtimehost.IMSRegistrationResult, error) {
		return runtimehost.IMSRegistrationResult{}, refreshErr
	}}}
	attempts := 0
	call, result, attemptErr := run.startMediaCallWithRecovery(t.Context(), func(runtimehost.IMSRegistrationResult) (VoiceCall, voicehost.OutboundCallResult, error) {
		attempts++
		return nil, voicehost.OutboundCallResult{FinalRejected: true, StatusCode: 503, RegistrationRecoveryNeeded: true}, nil
	})
	call, err := classifyMediaCallAttempt(call, result, attemptErr)
	if call != nil || attempts != 1 || !errors.Is(err, errConfirmedCallRejection) || !errors.Is(err, refreshErr) {
		t.Fatalf("exact rejection lost or replayed: call=%T attempts=%d err=%v", call, attempts, err)
	}
}
