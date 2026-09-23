package service

import (
	"errors"
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
