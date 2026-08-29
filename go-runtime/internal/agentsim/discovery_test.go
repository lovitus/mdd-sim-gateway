package agentsim

import (
	"bytes"
	"context"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
)

func TestDiscoveryRoutesExactSecondSecureElementWithoutRefreshingSession(t *testing.T) {
	secondEID := "89049032000000000000000000000002"
	card := estkDualCard(t, testEID, secondEID)
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	calls := 0
	manager.discoverProfiles = func(_ context.Context, gotCard Card, request agentlink.EUICCDiscoveryRequest,
		aid []byte) (string, []agentlink.EUICCDiscoveryEntry, error) {
		calls++
		if gotCard != card || request.EID != secondEID || request.IMEI != "123456789012345" ||
			!bytes.Equal(aid, estkSE1AID) {
			t.Fatalf("card=%p request=%+v aid=%X", gotCard, request, aid)
		}
		return defaultSMDSAddress, []agentlink.EUICCDiscoveryEntry{{
			EventID: "event-1", RSPServerAddress: "rsp.example.com",
		}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-1"})
	}()
	waitForSession(t, manager, "insertion-1")
	result := manager.ExecuteEUICCDiscovery(context.Background(), agentlink.EUICCDiscoveryRequest{
		OperationID: "discovery-se2", SessionGeneration: "insertion-1", EID: secondEID,
		IMEI: "123456789012345",
	})
	if result.Failure != nil || result.SMDS != defaultSMDSAddress || len(result.Entries) != 1 || calls != 1 ||
		len(manager.Sessions()) != 1 {
		t.Fatalf("result=%+v calls=%d sessions=%+v", result, calls, manager.Sessions())
	}
	wrong := manager.ExecuteEUICCDiscovery(context.Background(), agentlink.EUICCDiscoveryRequest{
		OperationID: "discovery-wrong-eid", SessionGeneration: "insertion-1",
		EID: "89049032000000000000000000000003",
	})
	if wrong.Failure == nil || wrong.Failure.Code != "euicc_identity_mismatch" || calls != 1 {
		t.Fatalf("wrong=%+v calls=%d", wrong, calls)
	}
	cancel()
	<-done
}
