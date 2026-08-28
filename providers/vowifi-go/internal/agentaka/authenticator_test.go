// SPDX-License-Identifier: AGPL-3.0-only

package agentaka

import (
	"context"
	"errors"
	"testing"

	swusim "github.com/boa-z/vowifi-go/engine/sim"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

type fakeBroker struct {
	request agentlink.AKARequest
	result  agentlink.AKAResponse
	err     error
}

func (broker *fakeBroker) AuthenticateAKA(_ context.Context, agentID, generation string, request agentlink.AKARequest) (agentlink.AKAResponse, error) {
	if agentID != "agent-1" || generation != "process-2" {
		return agentlink.AKAResponse{}, errors.New("wrong Agent identity")
	}
	broker.request = request
	result := broker.result
	result.OperationID = request.OperationID
	result.SessionGeneration = request.SessionGeneration
	var remote *agentlink.RemoteError
	if errors.As(broker.err, &remote) {
		copy := *remote
		result.Failure = &copy
	}
	return result, broker.err
}

func TestAuthenticateAKAParsesExactCardResponse(t *testing.T) {
	broker := &fakeBroker{result: agentlink.AKAResponse{
		Body: append([]byte{0xDB, 0x08}, append(bytesOf(0x10, 8), append([]byte{0x10}, append(bytesOf(0x20, 16), append([]byte{0x10}, bytesOf(0x30, 16)...)...)...)...)...),
		SW1:  0x90, SW2: 0x00,
	}}
	auth, err := New(broker, Config{
		AgentID: "agent-1", ProcessGeneration: "process-2",
		SessionGeneration: "card-3", CardID: "8944100000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := auth.AuthenticateAKA(swusim.AKAAuthRequest{
		Application: swusim.AKAApplicationISIM,
		RAND:        bytesOf(0x40, 16), AUTN: bytesOf(0x50, 16),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RES) != 8 || len(result.CK) != 16 || len(result.IK) != 16 {
		t.Fatalf("unexpected parsed AKA lengths: RES=%d CK=%d IK=%d", len(result.RES), len(result.CK), len(result.IK))
	}
	if broker.request.SessionGeneration != "card-3" || broker.request.CardID != "8944100000000000001" ||
		broker.request.Application != agentlink.AKAApplicationISIM || len(broker.request.OperationID) != 36 {
		t.Fatalf("wrong broker request: %+v", broker.request)
	}
}

func TestAuthenticateAKAPreservesSyncFailure(t *testing.T) {
	broker := &fakeBroker{result: agentlink.AKAResponse{
		Body: append([]byte{0xDC, 0x0E}, bytesOf(0x60, 14)...), SW1: 0x90, SW2: 0x00,
	}}
	auth, err := New(broker, Config{
		AgentID: "agent-1", ProcessGeneration: "process-2",
		SessionGeneration: "card-3", CardID: "8944100000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := auth.AuthenticateAKA(swusim.AKAAuthRequest{
		Application: swusim.AKAApplicationUSIM,
		RAND:        bytesOf(0x40, 16), AUTN: bytesOf(0x50, 16),
	})
	if !errors.Is(err, swusim.ErrSyncFailure) || len(result.AUTS) != 14 {
		t.Fatalf("result=%+v err=%v, want sync failure with AUTS", result, err)
	}
}

func TestAuthenticateAKARejectsBrokerFailure(t *testing.T) {
	broker := &fakeBroker{err: &agentlink.RemoteError{Kind: "not_ready", Code: "card_removed", Retryable: true}}
	auth, err := New(broker, Config{
		AgentID: "agent-1", ProcessGeneration: "process-2",
		SessionGeneration: "card-3", CardID: "8944100000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.AuthenticateAKA(swusim.AKAAuthRequest{
		Application: swusim.AKAApplicationUSIM,
		RAND:        bytesOf(0x40, 16), AUTN: bytesOf(0x50, 16),
	})
	var remote *agentlink.RemoteError
	if !errors.As(err, &remote) || remote.Code != "card_removed" {
		t.Fatalf("err=%v, want typed card_removed", err)
	}
}

func bytesOf(value byte, count int) []byte {
	out := make([]byte, count)
	for index := range out {
		out[index] = value
	}
	return out
}
